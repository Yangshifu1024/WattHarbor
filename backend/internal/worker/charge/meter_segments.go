package charge

import (
	"context"
	"encoding/json"
	"time"

	"chargepilot/internal/charging/measurement"
	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/protocol"
)

// measuredSegments 接收原启动时刻 start、结束 terminal及原序 events，将协议电量／时长读数交领域计量规则，返回分段或 nil无有效证据。
// 心跳 ChargedMWh 以 mWh 表示，计量规则换算为 Wh；功率以 0.1 W 表示，原测量转换四舍五入为 W；纯计算无 IO、不补造读数或实际收费。
func measuredSegments(start time.Time, terminal protocol.Event, events []protocol.Event) []charging.MeterSegment {
	span, ok := measurement.SessionSpanOf(terminal)
	if !ok {
		return nil
	}
	return charging.MeasuredSegments(start, span, measurement.ReadingSamplesOf(events))
}

// meterSegments 在 ctx 下使用 gateway、command、endID与 end读取原 ack_at至 end.ReceivedAt且 ID不超过 endID的心跳证据。
// 缺启动时刻或确认时间、超过10080条、领域证据不成立均返回 nil,nil；读取或载荷解码失败返回错误，不写数据库。
// 采样按原序交 measuredSegments，本次候选与跨尝试冻结权威区分，不推断缺失读数为零电量。
func (s EndSynchronizer) meterSegments(ctx context.Context, gateway EndGateway, command gatewaycontract.SyncCommand, endID uint64, end protocol.Event) ([]charging.MeterSegment, error) {
	if command.AckAt == nil || end.StartedAt.IsZero() {
		return nil, nil
	}
	// 按结束事件 ID固定本次心跳取证上界；跨尝试的计量权威由原网关冻结载荷保证。
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

// freezeEndResult 接收 ctx、gateway、command、原事件 id、event与 candidate，重算候选分段并先请求网关冻结，再解码返回网关权威结束请求。
// 取证、序列化、冻结或解码失败返回当前候选与错误；存量身份比较及首写优先由网关拥有，本层不补偿或重试未知写入。
// 冻结请求复用原事件／订单身份，存量载荷保留首次分段；成功不代表中央已确认或端口可释放。
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
