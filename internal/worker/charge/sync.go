package charge

import (
	"context"
	"errors"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// Synchronizer 只在 central 确认回执持久化后标记上报，不持有数据库或 HTTP 配置。
type Synchronizer struct {
	Gateway interface {
		StartResults(context.Context, string) ([]gatewaycontract.SyncCommand, error)
		MarkStartResultsReported(context.Context, []string) (int, error)
		CompensateStart(context.Context, string, string) error
	}
	Central interface {
		SubmitStartResult(context.Context, centralcontract.StartResultRequest) (centralcontract.ResultReply, error)
	}
	CommandFilter string
}

var ErrCentralConflict = centralcontract.ErrResultConflict

func (s Synchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.Gateway == nil || s.Central == nil {
		return 0, errors.New("start result synchronizer is not configured")
	}
	rows, err := s.Gateway.StartResults(ctx, s.CommandFilter)
	if err != nil {
		return 0, err
	}
	count := 0
	var firstError error
	reported := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.PortID == nil || *row.PortID <= 0 || row.ResultCode == nil || row.AckAt == nil || *row.ResultCode < 0 || *row.ResultCode > 255 {
			firstError = errors.New("invalid persisted start result")
			continue
		}
		result := centralcontract.StartResultRequest{CommandID: row.CommandID, ChargeOrderID: row.ChargeOrderID,
			OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo,
			PortID: uint64(*row.PortID), Success: row.Status == "acked",
			ResultCode: uint8(*row.ResultCode), OccurredAt: row.AckAt.UTC()}
		if result.Success != (result.ResultCode == 0) {
			firstError = errors.New("inconsistent persisted start result")
			continue
		}
		if result.ResultCode > 3 {
			// 补偿停止仅证明输出已停止，不能证明从未启动；保留支付待确认状态。
			firstError = errors.New("startup outcome needs review; compensation is not a no-charge proof")
			continue
		}
		if _, err := s.Central.SubmitStartResult(ctx, result); err != nil {
			if errors.Is(err, ErrCentralConflict) && result.Success {
				if compensateErr := s.Gateway.CompensateStart(ctx, result.OrderNo, result.CommandID); compensateErr != nil && firstError == nil {
					firstError = compensateErr
				}
				continue
			}
			if firstError == nil {
				firstError = err
			}
			continue
		}
		reported = append(reported, row.CommandID)
		count++
	}
	if len(reported) > 0 {
		if _, err := s.Gateway.MarkStartResultsReported(ctx, reported); err != nil {
			return count, err
		}
	}
	return count, firstError
}
