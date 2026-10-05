package charge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/charging/measurement"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// measuredSegments 把协议事件翻译成领域读数后交给 pricing 分段。
// 翻译在用例层完成，计费规则本身不感知协议。
func measuredSegments(start time.Time, terminal protocol.Event, events []protocol.Event) []charging.MeterSegment {
	span, ok := measurement.SessionSpanOf(terminal)
	if !ok {
		return nil
	}
	return charging.MeasuredSegments(start, span, measurement.ReadingSamplesOf(events))
}

// meterSegments 经 gateway 拉取计量证据：心跳在 [ack_at, end.ReceivedAt]
// 内且 id 不超过结束事件的行，与旧直读实现同界。返回超过 10080 行时放弃
// 分段（与旧实现一致，交由无分段路径处理）。
func (s EndSynchronizer) meterSegments(ctx context.Context, gateway EndGateway, command gatewaycontract.SyncCommand, endID uint64, end protocol.Event) ([]charging.MeterSegment, error) {
	if command.AckAt == nil || end.StartedAt.IsZero() {
		return nil, nil
	}
	// 以 BB 事件 ID 固定心跳证据范围，排除结束后落库的延迟心跳，保证重放结果一致。
	items, err := gateway.MeterSamples(ctx, gatewaycontract.MeterSamplesRequest{DeviceID: end.DeviceID, AckAt: *command.AckAt, EndAt: end.ReceivedAt, EndID: endID})
	if err != nil {
		return nil, err
	}
	if len(items) > 10080 {
		return nil, nil
	}
	samples := make([]protocol.Event, 0, len(items))
	for _, row := range items {
		var event protocol.Event
		if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
			return nil, err
		}
		samples = append(samples, event)
	}
	return measuredSegments(*command.AckAt, end, samples), nil
}

// freezeEndResult 在第一次 HTTP 尝试之前就把整份内部请求冻结。
// 无论是延迟到达的遥测还是保留期清理，
// 都不允许改动一份重放出去的结束回执。
// 冻结与身份校验在 gateway 单库内完成（忽略 segments 后语义一致即可）；
// 本组件每次重放都重新计算候选 segments，冻结端点保证首写优先。
func (s EndSynchronizer) freezeEndResult(ctx context.Context, gateway EndGateway, command gatewaycontract.SyncCommand, id uint64, event protocol.Event, candidate centralcontract.EndResultRequest) (centralcontract.EndResultRequest, error) {
	segments, err := s.meterSegments(ctx, gateway, command, id, event)
	if err != nil {
		return candidate, err
	}
	candidate.Meter.Segments = segments
	payload, err := json.Marshal(candidate)
	if err != nil {
		return candidate, err
	}
	reply, err := gateway.FreezeEndDelivery(ctx, gatewaycontract.FreezeEndDeliveryRequest{DeviceEventID: id, ChargeOrderID: candidate.ChargeOrderID, Payload: string(payload)})
	if err != nil {
		return candidate, err
	}
	var frozen centralcontract.EndResultRequest
	if err := json.Unmarshal([]byte(reply.Payload), &frozen); err != nil {
		return candidate, err
	}
	return frozen, nil
}
