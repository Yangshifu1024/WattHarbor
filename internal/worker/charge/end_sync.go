package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// EndGateway 是消费方结束同步窄接口，提供取证、首写冻结、原占用释放及处理标记；执行顺序由 EndSynchronizer拥有。
type EndGateway interface {
	// EndEvents 接收 ctx，返回原未处理结束事件页或读取错误；不修改事件。
	EndEvents(context.Context) ([]gatewaycontract.SyncEvent, error)
	// ChargeCommand 接收 ctx 与原订单 ID，返回关联 START命令、nil缺失或错误；不补造启动凭据。
	ChargeCommand(context.Context, uint64) (*gatewaycontract.SyncCommand, error)
	// MeterSamples 接收 ctx 与固定原设备／UTC窗口／事件ID上界请求，返回原心跳载荷或取证错误；无写入。
	MeterSamples(context.Context, gatewaycontract.MeterSamplesRequest) ([]gatewaycontract.SyncEvent, error)
	// FreezeEndDelivery 接收 ctx 与原事件／订单／候选载荷，返回首写或既有冻结 Reply及错误；冻结与身份比较由网关执行。
	FreezeEndDelivery(context.Context, gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryReply, error)
	// ReleasePort 接收 ctx 与原端口／订单释放请求，返回释放或已释放 Reply及错误；占用条件保护由网关执行。
	ReleasePort(context.Context, gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortReply, error)
	// MarkDeviceEventsProcessed 接收 ctx 与原事件 ID集合，返回实际处理标记数或错误；持久标记由网关事务拥有。
	MarkDeviceEventsProcessed(context.Context, []uint64) (int, error)
}

// EndSynchronizer 编排原设备结束证据向中央确认并推进网关投递状态；依赖由 cmd注入，不持数据库、服务URL或进程资源。
type EndSynchronizer struct {
	// Gateway 原结束同步所需网关窄能力，执行者由 cmd注入；不在 worker持网关数据库或 URL。
	Gateway EndGateway
	// Central 原中央结束回执确认能力，事务及结束计量／计费排队由中央用例拥有。
	Central interface {
		// SubmitEndResult 接收 ctx 与原冻结结束请求，返回接受／重放 Reply或错误；不代表计费、扣款或退款已完成。
		SubmitEndResult(context.Context, centralcontract.EndResultRequest) (centralcontract.ResultReply, error)
	}
}

// SyncBatch 接收 ctx，逐个核对结束事件与已 acked START身份，先获取并冻结原计量请求，再提交中央，确认后释放原端口，最后批量标记事件。
// 返回已获释放或已释放确认的条数与原错误；解析、取证、冻结及中央失败继续其它事件，释放失败立即返回并不提交已收集标记。
// 标记失败仍返回已有计数，计数不表示事件标记已提交；所有调用复用 ctx、原事件和命令身份，不直接计费或自动创建恢复身份。
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
