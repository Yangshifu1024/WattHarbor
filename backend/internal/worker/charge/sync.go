package charge

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
)

// Synchronizer 向中央提交启动回执，确认成功后标记已上报；启动成功但中央拒绝时请求停机补偿。
type Synchronizer struct {
	// Gateway 启动回执读取、上报标记和启动补偿接口，按命令标识执行。
	Gateway interface {
		// StartResults 查询待上报的启动回执，可按命令编号筛选。
		StartResults(context.Context, string) ([]gatewaycontract.SyncCommand, error)
		// MarkStartResultsReported 在网关事务内标记命令已上报，返回影响行数。
		MarkStartResultsReported(context.Context, []string) (int, error)
		// CompensateStart 为指定 START 请求停机补偿，不重新启动或决定退款。
		CompensateStart(context.Context, string, string) error
	}
	// Central 中央启动回执确认接口，不在 worker 执行订单或资金算法。
	Central interface {
		// SubmitStartResult 由中央保存启动结果并更新订单，重复请求复用同一回执。
		SubmitStartResult(context.Context, centralcontract.StartResultRequest) (centralcontract.ResultReply, error)
	}
	// CommandFilter 可选启动命令筛选值，原样交网关读取，不改变回执身份或生成游标。
	CommandFilter string
}

var ErrCentralConflict = centralcontract.ErrResultConflict

// SyncBatch 校验启动回执，提交中央成功后才批量标记已上报，返回中央已确认数量和处理错误。
// 标记失败时仍保留确认数量，不表示标记已提交。
// 中央冲突仅对成功 START 请求停机补偿，不标记该回执；补偿成功可返回 nil。
// 大于 3 的结果码保留待核实；STOP 结果不代表资金已满足退款条件。
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
			// 结果码大于 3 保留待核实，不提交中央确认或上报标记；补偿请求不作为从未输出的凭据。
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
