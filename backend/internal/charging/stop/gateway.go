package stop

import (
	"context"
	"errors"
	"fmt"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// Gateway 是停止用例所需的设备侧能力；实现负责传输与响应契约校验。
type Gateway interface {
	// ChargingEvidence 在 context 期限内按请求顺序返回完整页及推进游标；传输或契约失败返回错误。
	ChargingEvidence(context.Context, []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
	// RequestStop 在 context 期限内用原订单身份请求持久 STOP；返回受理凭据或错误，不证明设备结束。
	RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopReply, error)
}

// ErrEvidenceIncomplete 表示取证响应身份、分页游标或页数不可信，不能据此停机。
var ErrEvidenceIncomplete = errors.New("charging evidence incomplete")

// 单批订单取证的最多翻页次数；7 天窗口按每分钟一条心跳计通常一页足够，
// 翻页上限防止异常高密度心跳把一次停机扫描拖死。
const autoStopEvidenceMaxPages = 8

// evidence 按订单分批向 gateway 取证，返回订单 ID 到心跳样本的映射。
// ctx 限制网络调用，orders 提供目标身份和起始时刻；无订单返回空映射。
// 错误表示整批证据不完整，此时不返回可用于决策的部分样本。
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

// requestStop 向 gateway 下发停机；gateway 受理后异步回执，
// ctx 取消网络调用，order 提供不可变订单号与用户身份；错误时下一轮沿用原截止点重试。
// 本调用只确认意图被受理（202），不确认端口已停。
func (s AutoStopper) requestStop(ctx context.Context, order AutoStopOrder) error {
	_, err := s.Gateway.RequestStop(ctx, gatewaycontract.StopRequest{
		OrderNo: order.OrderNo, UserID: order.UserID, Source: gatewaycontract.StopSourceAuto,
	})
	return err
}
