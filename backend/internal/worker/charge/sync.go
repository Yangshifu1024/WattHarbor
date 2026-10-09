// Package charge 启动已支付订单、同步设备结果并恢复刷卡事件处理。
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
		// DeferSyncDelivery 保存本次失败分类和网关持久退避，不变更命令业务状态。
		DeferSyncDelivery(context.Context, gatewaycontract.DeferSyncDeliveryRequest) error
		// StartResults 查询待上报的启动回执，可按命令编号筛选。
		StartResults(context.Context, string) ([]gatewaycontract.SyncCommand, error)
		// MarkStartResultsReported 在网关事务内标记命令已上报，返回影响行数。
		MarkStartResultsReported(context.Context, []string) (int, error)
		// CompensateStart 为指定 START 请求停机补偿，不重新启动或决定退款。
		CompensateStart(context.Context, string, string) error
	}
	// Central 保存启动回执并更新订单状态。
	Central interface {
		// SubmitStartResult 由中央保存启动结果并更新订单，重复请求复用同一回执。
		SubmitStartResult(context.Context, centralcontract.StartResultRequest) (centralcontract.ResultReply, error)
	}
	// CommandFilter 指定要读取的启动命令；空值读取所有待上报回执。
	CommandFilter string
}

// ErrCentralConflict 表示中央服务拒绝与订单不匹配的启动回执。
var ErrCentralConflict = centralcontract.ErrResultConflict

// SyncBatch 核对启动回执，提交中央成功后批量标记已上报，返回确认数量和错误。
// 中央拒绝已成功启动的命令时请求补偿停机，并保留待上报状态。
// 结果码大于 3 留待核实；标记失败时确认数量仍保留。
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
	fail := func(row gatewaycontract.SyncCommand, category string, cause error) {
		firstError = errors.Join(firstError, cause, s.Gateway.DeferSyncDelivery(ctx, gatewaycontract.DeferSyncDeliveryRequest{Kind: "start", Key: row.CommandID, Category: category}))
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return count, errors.Join(firstError, err)
		}
		if row.PortID == nil || *row.PortID <= 0 || row.ResultCode == nil || row.AckAt == nil || *row.ResultCode < 0 || *row.ResultCode > 255 {
			fail(row, "invalid", errors.New("invalid persisted start result"))
			continue
		}
		result := centralcontract.StartResultRequest{CommandID: row.CommandID, ChargeOrderID: row.ChargeOrderID,
			OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo,
			PortID: uint64(*row.PortID), Success: row.Status == "acked",
			ResultCode: uint8(*row.ResultCode), OccurredAt: row.AckAt.UTC()}
		if result.Success != (result.ResultCode == 0) {
			fail(row, "invalid", errors.New("inconsistent persisted start result"))
			continue
		}
		if result.ResultCode > 3 {
			// 结果码大于 3 无法确认启动结果，保留回执等待核实。
			fail(row, "unknown", errors.New("startup outcome needs review; compensation is not a no-charge proof"))
			continue
		}
		if _, err := s.Central.SubmitStartResult(ctx, result); err != nil {
			if errors.Is(err, ErrCentralConflict) && result.Success {
				compensateErr := s.Gateway.CompensateStart(ctx, result.OrderNo, result.CommandID)
				// 已受理补偿仍不能证明没有充电；进入核实，继续处理其他回执。
				fail(row, "unknown", errors.Join(err, compensateErr))
				continue
			}
			fail(row, "retry", err)
			continue
		}
		reported = append(reported, row.CommandID)
		count++
	}
	if len(reported) > 0 {
		if _, err := s.Gateway.MarkStartResultsReported(ctx, reported); err != nil {
			return count, errors.Join(firstError, err)
		}
	}
	return count, firstError
}
