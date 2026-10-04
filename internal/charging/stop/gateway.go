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
	ChargingEvidence(context.Context, []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error)
	RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error)
}

var ErrEvidenceIncomplete = errors.New("charging evidence incomplete")

// 单批订单取证的最多翻页次数；7 天窗口按每分钟一条心跳计通常一页足够，
// 翻页上限防止异常高密度心跳把一次停机扫描拖死。
const autoStopEvidenceMaxPages = 8

// evidence 按订单分批向 gateway 取证，返回订单 ID 到心跳样本的映射。
// 单批超过一页时按 next_after_id 续取，翻页上限 autoStopEvidenceMaxPages。
func (s AutoStopper) evidence(ctx context.Context, orders []AutoStopOrder) (map[uint64][]protocol.Event, error) {
	out := make(map[uint64][]protocol.Event, len(orders))
	for start := 0; start < len(orders); start += gatewaycontract.MaxEvidenceBatch {
		chunk := orders[start:min(start+gatewaycontract.MaxEvidenceBatch, len(orders))]
		requests := make([]gatewaycontract.EvidenceQuery, len(chunk))
		orderIDs := make([]uint64, len(chunk))
		for i, order := range chunk {
			requests[i] = gatewaycontract.EvidenceQuery{DeviceID: order.DeviceID, PortNo: order.PortNo, StartedAt: order.StartedAt}
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
			pending := make([]gatewaycontract.EvidenceQuery, 0, len(requests))
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
// 本调用只确认意图被受理（202），不确认端口已停。
func (s AutoStopper) requestStop(ctx context.Context, order AutoStopOrder) error {
	_, err := s.Gateway.RequestStop(ctx, gatewaycontract.StopRequest{
		OrderNo: order.OrderNo, UserID: order.UserID, Source: gatewaycontract.StopSourceAuto,
	})
	return err
}
