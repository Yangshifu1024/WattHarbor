package charge

import (
	"context"
	"encoding/json"
	"time"

	"wattharbor/internal/charging/measurement"
	centralcontract "wattharbor/internal/contracts/central"
	gatewaycontract "wattharbor/internal/contracts/gateway"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/protocol"
)

// measuredSegments 根据设备累计电量和时长计算各时间段的用电量。
// 心跳电量从 mWh 换算为 Wh，功率从 0.1 W 四舍五入为 W；读数无法验证时返回 nil。
func measuredSegments(start time.Time, terminal protocol.Event, events []protocol.Event) []charging.MeterSegment {
	span, ok := measurement.SessionSpanOf(terminal)
	if !ok {
		return nil
	}
	return charging.MeasuredSegments(start, span, measurement.ReadingSamplesOf(events))
}

// meterSegments 读取启动确认至结束事件接收期间的心跳，且 ID 不超过结束事件 ID。
// 缺少时间、超过 10080 条或读数无法验证时不生成分段；查询或 JSON 解码失败返回错误。
func (s EndSynchronizer) meterSegments(ctx context.Context, gateway EndGateway, command gatewaycontract.SyncCommand, endID uint64, end protocol.Event) ([]charging.MeterSegment, error) {
	if command.AckAt == nil || end.StartedAt.IsZero() {
		return nil, nil
	}
	// 只使用结束事件之前已保存的心跳，减少重试时新增读数的影响。
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

// freezeEndResult 生成分段并保存首次结束请求，后续处理读取同一份请求。
// 保存成功后还须提交中央服务，不能在此时释放端口。
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
