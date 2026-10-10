package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/protocol"
)

// EndGateway 提供结束事件、心跳读数、请求保存、端口释放及处理标记。
type EndGateway interface {
	// DeferSyncDelivery 保存事件失败分类与持久退避，冻结结束请求保持不变。
	DeferSyncDelivery(context.Context, gatewaycontract.DeferSyncDeliveryRequest) error
	// EndEvents 返回未处理结束事件页或读取错误；不修改事件。
	EndEvents(context.Context) ([]gatewaycontract.SyncEvent, error)
	// ChargeCommand 查询订单对应的 START 命令；不存在时返回 nil。
	ChargeCommand(context.Context, uint64) (*gatewaycontract.SyncCommand, error)
	// MeterSamples 查询指定设备、UTC 时间窗口和事件 ID 上限内的心跳。
	MeterSamples(context.Context, gatewaycontract.MeterSamplesRequest) ([]gatewaycontract.SyncEvent, error)
	// FreezeEndDelivery 保存首次结束请求；重复调用读取旧内容，关键字段不一致则拒绝。
	FreezeEndDelivery(context.Context, gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryReply, error)
	// ReleasePort 仅释放该订单仍占用的端口；已释放也返回成功。
	ReleasePort(context.Context, gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortReply, error)
	// MarkDeviceEventsProcessed 在网关事务内标记事件已处理，返回实际影响行数。
	MarkDeviceEventsProcessed(context.Context, []uint64) (int, error)
}

// EndSynchronizer 将设备结束结果提交中央服务，确认后释放端口并标记事件。
type EndSynchronizer struct {
	// Gateway 读取和更新网关中的结束事件与端口。
	Gateway EndGateway
	// Central 保存订单结束结果并安排后续计费。
	Central interface {
		// SubmitEndResult 由中央确认结束请求；确认不代表计费、扣款或退款已完成。
		SubmitEndResult(context.Context, centralcontract.EndResultRequest) (centralcontract.ResultReply, error)
	}
}

// SyncBatch 核对结束事件与已确认的启动命令，再保存请求、提交中央、释放端口并标记事件。
// 读取、提交或释放失败时持久退避并继续独立事件；仅中央及释放均确认的项进入标记。
// 返回端口已释放的数量；标记失败时保留计数并返回错误。
func (s EndSynchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.Gateway == nil || s.Central == nil {
		return 0, errors.New("charge end synchronizer is not configured")
	}
	items, err := s.Gateway.EndEvents(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	var failures syncBatchErrors
	processed := make([]uint64, 0, len(items))
	fail := func(item gatewaycontract.SyncEvent, category string, cause error) {
		key := strconv.FormatUint(item.ID, 10)
		failures.add("end", key, cause, s.Gateway.DeferSyncDelivery(ctx, gatewaycontract.DeferSyncDeliveryRequest{Kind: "end", Key: key, Category: category}))
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return count, errors.Join(failures.err(), err)
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(item.Payload), &event); err != nil {
			fail(item, "invalid", err)
			continue
		}
		orderID, err := strconv.ParseUint(event.OrderNumber, 10, 64)
		if err != nil || orderID == 0 || (event.ConsumerType != 2 && event.ConsumerType != 3) || event.EndedAt.IsZero() {
			fail(item, "invalid", errors.New("invalid dc589 charge end event"))
			continue
		}
		command, err := s.Gateway.ChargeCommand(ctx, orderID)
		if err != nil {
			fail(item, "retry", err)
			continue
		}
		if command == nil {
			fail(item, "dependency", errors.New("charge end has no persisted start command"))
			continue
		}
		if command.ChargeOrderID != orderID || command.DeviceID != event.DeviceID || command.PortNo != event.Port || command.Status != "acked" || command.PortID == nil || *command.PortID <= 0 {
			fail(item, "unknown", errors.New("charge end does not match an acknowledged START"))
			continue
		}
		result := centralcontract.EndResultRequest{OrderNo: command.OrderNo, ChargeOrderID: orderID, StartCommandID: command.CommandID, StopCommandID: command.StopCommandID, DeviceID: event.DeviceID, PortNo: event.Port, PortID: uint64(*command.PortID)}
		result.Meter.ChargedWh, result.Meter.ChargedSeconds = event.EnergyMilliKWh, event.ChargedSeconds
		result.Meter.EndedAt, result.Meter.StopReason = event.EndedAt, event.StopReason
		result, err = s.freezeEndResult(ctx, s.Gateway, *command, item.ID, event, result)
		if err != nil {
			fail(item, "retry", err)
			continue
		}
		if _, err := s.Central.SubmitEndResult(ctx, result); err != nil {
			fail(item, "retry", err)
			continue
		}
		release, err := s.Gateway.ReleasePort(ctx, gatewaycontract.ReleasePortRequest{PortID: result.PortID, OrderNo: result.OrderNo})
		if err != nil || !release.Released {
			if err == nil {
				err = errors.New("gateway did not confirm port release")
			}
			fail(item, "retry", fmt.Errorf("gateway port could not be released after central acceptance: %w", err))
			continue
		}
		processed = append(processed, item.ID)
		count++
	}
	if len(processed) > 0 {
		if _, err := s.Gateway.MarkDeviceEventsProcessed(ctx, processed); err != nil {
			return count, errors.Join(failures.err(), err)
		}
	}
	return count, failures.err()
}
