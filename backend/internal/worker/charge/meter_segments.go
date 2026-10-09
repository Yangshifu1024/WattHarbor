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

// measuredSegments 将设备累计电量和时长转为计量分段；证据不足时返回 nil。
// 心跳 ChargedMWh 的单位是 mWh，分段电量换算为 Wh；功率以 0.1 W 上报，四舍五入为 W。
func measuredSegments(start time.Time, terminal protocol.Event, events []protocol.Event) []charging.MeterSegment {
	span, ok := measurement.SessionSpanOf(terminal)
	if !ok {
		return nil
	}
	return charging.MeasuredSegments(start, span, measurement.ReadingSamplesOf(events))
}

// meterSegments 读取 ack_at 至 end.ReceivedAt 的心跳，只取 ID 不超过 endID 的记录。
// 缺启动时刻或确认时间、超过 10080 条、领域证据不成立均返回 nil,nil；读取或载荷解码失败返回错误，不写数据库。
// 采样按序计算候选分段；重复尝试使用首次保存的分段，不把缺失读数当成零电量。
func (s EndSynchronizer) meterSegments(ctx context.Context, gateway EndGateway, command gatewaycontract.SyncCommand, endID uint64, end protocol.Event) ([]charging.MeterSegment, error) {
	if command.AckAt == nil || end.StartedAt.IsZero() {
		return nil, nil
	}
	// 结束事件 ID 限定本次心跳查询范围；多次尝试最终都使用网关首次保存的载荷。
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

// freezeEndResult 计算候选计量分段，请网关保存首次结束请求，并返回网关已保存的请求。
// 取证、序列化、冻结或解码失败返回当前候选与错误；存量身份比较及首次写入优先由网关拥有，本层不补偿或重试未知写入。
// 冻结请求复用事件／订单编号，存量载荷保留首次分段；成功不代表中央已确认或端口可释放。
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
