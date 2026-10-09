package query

import (
	"chargepilot/internal/card"
	"chargepilot/internal/charging/stop"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/settlement"
	"context"
	"errors"
	"gorm.io/gorm"
)

// UserStateRepo 组合各所属模块的只读能力；不直接访问其它模块的表或持有外部客户端。
type UserStateRepo struct {
	DB *gorm.DB // 同一中央库连接或调用者只读事务，由进程管理
}

// Frozen 在 ctx 下读取已授权 row 的冻结方案、计费进度、操作与截止，返回私有读模型或存储错误。
// 缺少记录保持原零状态，不使用当前方案补齐，也不执行资金或生命周期写入。
func (r UserStateRepo) Frozen(ctx context.Context, row order.ChargeOrderRecord) (FrozenState, error) {
	var out FrozenState
	snapshot, err := order.FindPricingSnapshot(ctx, r.DB, row.ID)
	if err != nil {
		return out, err
	}
	out.Snapshot = snapshot.PricingSnapshot
	job, err := settlement.FindBillingJob(ctx, r.DB, row.ID)
	if err != nil {
		return out, err
	}
	out.BillingStatus = job.Status
	ops, err := card.ListOrderOperations(ctx, r.DB, row.ID)
	if err != nil {
		return out, err
	}
	out.CardOperations = make([]CardOperation, 0, len(ops))
	for _, p := range ops {
		out.CardOperations = append(out.CardOperations, CardOperation{OperationID: p.OperationID, DeviceID: p.DeviceID, EventID: p.EventID, ChargeOrderID: p.ChargeOrderID, CardID: p.CardID, Kind: p.Kind, PriceCents: p.PriceCents, Minutes: p.Minutes, Status: p.Status})
	}
	cutoff, err := stop.FindBillingCutoff(ctx, r.DB, row.ID)
	if err != nil {
		return out, err
	}
	out.Cutoff = BillingCutoff{CutoffAt: cutoff.CutoffAt, Reason: cutoff.Reason}
	return out, nil
}

// Funds 在 ctx 下读取已授权 row 的原支付摘要及退款列表，返回原金额／空记录或分阶段存储错误。
// 不把支付存储故障当作无支付；不重试渠道、不执行退款或更新资金。
func (r UserStateRepo) Funds(ctx context.Context, row order.ChargeOrderRecord) (DetailFunds, error) {
	out := DetailFunds{Refunds: []map[string]any{}}
	if row.PaymentOrderID.Valid {
		p, err := payment.FindOrderPaymentBrief(ctx, r.DB, uint64(row.PaymentOrderID.Int64))
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return out, &UserReadError{Stage: "payment", Cause: err}
		}
		if err == nil {
			out.Payment = &PaymentBrief{OrderNo: p.OrderNo, TotalCents: p.TotalCents, PaidCents: p.PaidCents, RefundedCents: p.RefundedCents, Status: p.Status}
		}
	}
	rows, err := settlement.ListOrderRefunds(ctx, r.DB, row.ID)
	if err != nil {
		return out, &UserReadError{Stage: "refund", Cause: err}
	}
	out.Refunds = rows
	return out, nil
}
