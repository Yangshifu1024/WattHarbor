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

// EndGateway 提供结束事件、计量证据、首次结束请求保存、端口释放和事件标记接口。
type EndGateway interface {
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

// EndSynchronizer 编排设备结束证据向中央确认并更新网关投递状态；依赖由 cmd 注入，不持数据库、服务 URL 或进程资源。
type EndSynchronizer struct {
	// Gateway 结束同步所需网关接口，执行者由 cmd 注入；不在 worker 持网关数据库或 URL。
	Gateway EndGateway
	// Central 中央结束回执确认能力，事务及结束计量／计费排队由中央用例拥有。
	Central interface {
		// SubmitEndResult 由中央确认结束请求；确认不代表计费、扣款或退款已完成。
		SubmitEndResult(context.Context, centralcontract.EndResultRequest) (centralcontract.ResultReply, error)
	}
}

// SyncBatch 核对结束事件对应已确认的 START，再依次保存结束请求、提交中央、释放端口、批量标记事件。
// 取证、保存或中央提交失败时继续下一事件；释放失败立即返回，不提交此前收集的标记。
// 返回数只表示端口已释放或已是释放状态；标记失败时仍保留计数，不表示标记已提交。
func (s EndSynchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.Gateway == nil || s.Central == nil {
		return 0, errors.New("charge end synchronizer is not configured")
	}
	items, err := s.Gateway.EndEvents(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	var first error
	processed := make([]uint64, 0, len(items))
	for _, item := range items {
		var event protocol.Event
		if err := json.Unmarshal([]byte(item.Payload), &event); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		orderID, err := strconv.ParseUint(event.OrderNumber, 10, 64)
		if err != nil || orderID == 0 || (event.ConsumerType != 2 && event.ConsumerType != 3) || event.EndedAt.IsZero() {
			if first == nil {
				first = errors.New("invalid dc589 charge end event")
			}
			continue
		}
		command, err := s.Gateway.ChargeCommand(ctx, orderID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if command == nil {
			if first == nil {
				first = errors.New("charge end has no persisted start command")
			}
			continue
		}
		if command.ChargeOrderID != orderID || command.DeviceID != event.DeviceID || command.PortNo != event.Port || command.Status != "acked" || command.PortID == nil || *command.PortID <= 0 {
			if first == nil {
				first = errors.New("charge end does not match an acknowledged START")
			}
			continue
		}
		result := centralcontract.EndResultRequest{OrderNo: command.OrderNo, ChargeOrderID: orderID, StartCommandID: command.CommandID, StopCommandID: command.StopCommandID, DeviceID: event.DeviceID, PortNo: event.Port, PortID: uint64(*command.PortID)}
		result.Meter.ChargedWh, result.Meter.ChargedSeconds = event.EnergyMilliKWh, event.ChargedSeconds
		result.Meter.EndedAt, result.Meter.StopReason = event.EndedAt, event.StopReason
		result, err = s.freezeEndResult(ctx, s.Gateway, *command, item.ID, event, result)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if _, err := s.Central.SubmitEndResult(ctx, result); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		release, err := s.Gateway.ReleasePort(ctx, gatewaycontract.ReleasePortRequest{PortID: result.PortID, OrderNo: result.OrderNo})
		if err != nil || !release.Released {
			if err == nil {
				err = errors.New("gateway port could not be released after settlement")
			}
			return count, fmt.Errorf("gateway port could not be released after settlement: %w", err)
		}
		processed = append(processed, item.ID)
		count++
	}
	if len(processed) > 0 {
		if _, err := s.Gateway.MarkDeviceEventsProcessed(ctx, processed); err != nil {
			return count, err
		}
	}
	return count, first
}
