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

// EndGateway 是结束同步所需的网关操作；冻结、释放与处理标记顺序由消费者负责。
type EndGateway interface {
	EndEvents(context.Context) ([]gatewaycontract.SyncEvent, error)
	ChargeCommand(context.Context, uint64) (*gatewaycontract.SyncCommand, error)
	MeterSamples(context.Context, gatewaycontract.MeterSamplesRequest) ([]gatewaycontract.SyncEvent, error)
	FreezeEndDelivery(context.Context, gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryReply, error)
	ReleasePort(context.Context, gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortReply, error)
	MarkDeviceEventsProcessed(context.Context, []uint64) (int, error)
}

type EndSynchronizer struct {
	Gateway EndGateway
	Central interface {
		SubmitEndResult(context.Context, centralcontract.EndResultRequest) (centralcontract.ResultReply, error)
	}
}

// SyncBatch 先冻结回执，central 确认后释放端口，再标记结束事件；失败保留重放入口。
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
