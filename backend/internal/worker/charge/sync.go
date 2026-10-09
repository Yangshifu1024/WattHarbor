package charge

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
)

// Synchronizer 在中央确认原启动结果后请求标记上报，必要时按原 START身份请求补偿；不持数据库、HTTP配置或进程资源。
type Synchronizer struct {
	// Gateway 原启动回执读取、上报标记和启动补偿窄能力，按原命令身份执行。
	Gateway interface {
		// StartResults 接收 ctx 与可选命令筛选，返回原启动凭据列表或读取错误；只读，不标记。
		StartResults(context.Context, string) ([]gatewaycontract.SyncCommand, error)
		// MarkStartResultsReported 接收 ctx 与原命令集合，返回标记推进数或错误；实际持久副作用由网关拥有。
		MarkStartResultsReported(context.Context, []string) (int, error)
		// CompensateStart 接收 ctx、原订单号与 START命令，执行原补偿请求并返回错误；不生成第二条 START，不证明资金可退款。
		CompensateStart(context.Context, string, string) error
	}
	// Central 原中央启动回执确认窄能力，不在 worker执行订单或资金算法。
	Central interface {
		// SubmitStartResult 接收 ctx 与原启动结果，返回接受／重放 Reply或错误；中央拥有原订单状态及回执事务。
		SubmitStartResult(context.Context, centralcontract.StartResultRequest) (centralcontract.ResultReply, error)
	}
	// CommandFilter 可选原启动命令筛选值，原样交网关读取，不改变回执身份或生成游标。
	CommandFilter string
}

var ErrCentralConflict = centralcontract.ErrResultConflict

// SyncBatch 接收 ctx，读取原启动回执，校验可空端口／结果码／确认时刻后按原身份提交中央，并在确认成功后批量标记上报。
// 返回中央确认数与原错误；标记失败仍保留此前确认数，计数不能代替标记已提交。单条无效或失败继续其它记录。
// 中央冲突仅对成功 START 请求原补偿且不标记，补偿成功可返回 nil；异常码大于3保留待核实，不据 STOP事实推断可退款。
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
			// 结果码大于3保留待核实，不提交中央确认或上报标记；补偿请求不作为从未输出的凭据。
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
