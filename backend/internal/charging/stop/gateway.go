package stop

import (
	"context"
	"errors"
	"fmt"

	gatewaycontract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/protocol"
)

// Gateway 查询设备心跳并提交停止请求，实现方负责网络调用和响应校验。
type Gateway interface {
	// ChargingEvidence 在 context 期限内按请求顺序返回完整页及更新游标；传输或契约失败返回错误。
	ChargingEvidence(context.Context, []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
	// RequestStop 用订单编号请求网关保存 STOP 命令，返回受理结果；设备停止由回执确认。
	RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopReply, error)
}

// ErrEvidenceIncomplete 表示心跳响应的设备、端口、游标或页数不合要求，不能用于停机判断。
var ErrEvidenceIncomplete = errors.New("charging evidence incomplete")

// autoStopEvidenceMaxPages 限制每组订单的心跳查询页数，避免异常密集上报拖延整轮扫描。
const autoStopEvidenceMaxPages = 8

// evidence 按订单分批查询 gateway 心跳，返回订单 ID 到心跳列表的映射。
// ctx 限制网络调用，orders 提供目标身份和起始时刻；无订单返回空映射。
// 任一批响应异常时返回错误，丢弃本次已查询样本，不据部分数据判断停机。
// 单批超过一页时按 next_after_id 续取，翻页上限 autoStopEvidenceMaxPages。
func (s AutoStopper) evidence(ctx context.Context, orders []AutoStopOrder) (map[uint64][]protocol.Event, error) {
	out := make(map[uint64][]protocol.Event, len(orders))
	for start := 0; start < len(orders); start += gatewaycontract.MaxEvidenceBatch {
		chunk := orders[start:min(start+gatewaycontract.MaxEvidenceBatch, len(orders))]
		requests := make([]gatewaycontract.EvidenceQueryRequest, len(chunk))
		orderIDs := make([]uint64, len(chunk))
		for i, order := range chunk {
			requests[i] = gatewaycontract.EvidenceQueryRequest{DeviceID: order.DeviceID, PortNo: order.PortNo, StartedAt: order.StartedAt}
			orderIDs[i] = order.ID
		}
		for page := 0; page < autoStopEvidenceMaxPages; page++ {
			pages, err := s.Gateway.ChargingEvidence(ctx, requests)
			if err != nil {
				return nil, err
			}
			if len(pages) != len(requests) {
				return nil, fmt.Errorf("%w: unexpected page count", ErrEvidenceIncomplete)
			}
			pending := make([]gatewaycontract.EvidenceQueryRequest, 0, len(requests))
			pendingIDs := make([]uint64, 0, len(requests))
			for i, entry := range pages {
				query := requests[i]
				if entry.DeviceID != query.DeviceID || entry.PortNo != query.PortNo {
					return nil, fmt.Errorf("%w: unexpected device or port", ErrEvidenceIncomplete)
				}
				for _, sample := range entry.Samples {
					out[orderIDs[i]] = append(out[orderIDs[i]], protocol.Event{
						Type: sample.Type, DeviceID: sample.DeviceID,
						ReceivedAt: sample.ReceivedAt, ChargingPorts: sample.ChargingPorts,
					})
				}
				if entry.Complete {
					continue
				}
				if entry.NextAfterID <= query.AfterID {
					return nil, fmt.Errorf("%w: cursor did not advance", ErrEvidenceIncomplete)
				}
				query.AfterID = entry.NextAfterID
				pending = append(pending, query)
				pendingIDs = append(pendingIDs, orderIDs[i])
			}
			requests, orderIDs = pending, pendingIDs
			if len(requests) == 0 {
				break
			}
		}
		if len(requests) > 0 {
			return nil, fmt.Errorf("%w: page limit exceeded", ErrEvidenceIncomplete)
		}
	}
	return out, nil
}

// requestStop 向 gateway 下发停机；gateway 受理后异步回执。
// ctx 取消网络调用，order 提供不可变订单号与用户 ID；错误时下一轮沿用截止点重试。
// 本调用只确认意图被受理（202），不确认端口已停。
func (s AutoStopper) requestStop(ctx context.Context, order AutoStopOrder) error {
	_, err := s.Gateway.RequestStop(ctx, gatewaycontract.StopRequest{
		OrderNo: order.OrderNo, UserID: order.UserID, Source: gatewaycontract.StopSourceAuto,
	})
	return err
}
