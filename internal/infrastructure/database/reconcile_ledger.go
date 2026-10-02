package database

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"gorm.io/gorm"
)

// 四类内部台账的取数口径。每类按参照号聚合当天金额，供对账逐笔比对。

// internalRefunds 取当天支付成功、且退款已完成的退款记录，按退款单号聚合金额，供微信退款对账。
func InternalRefunds(ctx context.Context, db *gorm.DB, date time.Time) (map[string]int64, error) {
	rows := []struct {
		RefundNo    string
		RefundCents int64
	}{}
	if err := db.WithContext(ctx).Table("refund_record AS r").
		Joins("JOIN payment_order AS p ON p.id = r.payment_order_id").
		Where("DATE(p.paid_at) = ? AND r.status = 'succeeded' AND r.deleted_at IS NULL", date.Format("2006-01-02")).
		Select("r.refund_no, r.refund_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return money.IndexAmounts(rows, func(i int) (string, int64) { return rows[i].RefundNo, rows[i].RefundCents }), nil
}

// internalPayments 取当天已收款以及之后发生退款的支付单，按支付单号聚合金额，供微信支付对账。
func InternalPayments(ctx context.Context, db *gorm.DB, date time.Time) (map[string]int64, error) {
	rows := []struct {
		OrderNo   string
		PaidCents int64
	}{}
	if err := db.WithContext(ctx).Table("payment_order").
		Where("DATE(paid_at) = ? AND status IN ('paid','partial_refunded','refunded') AND deleted_at IS NULL", date.Format("2006-01-02")).
		Select("order_no, paid_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return money.IndexAmounts(rows, func(i int) (string, int64) { return rows[i].OrderNo, rows[i].PaidCents }), nil
}

// internalSplits 取当天生成的分账汇总，按分账单号聚合金额，供分账对账。
func InternalSplits(ctx context.Context, db *gorm.DB, date time.Time) (map[string]int64, error) {
	rows := []struct {
		SettlementNo string
		TotalCents   int64
	}{}
	if err := db.WithContext(ctx).Table("settlement").
		Where("DATE(created_at) = ?", date.Format("2006-01-02")).Select("settlement_no, total_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return money.IndexAmounts(rows, func(i int) (string, int64) { return rows[i].SettlementNo, rows[i].TotalCents }), nil
}

// internalWithdraws 取当天创建的提现单，按提现单号聚合金额，供提现对账。
func InternalWithdraws(ctx context.Context, db *gorm.DB, date time.Time) (map[string]int64, error) {
	rows := []struct {
		WithdrawNo  string
		AmountCents int64
	}{}
	if err := db.WithContext(ctx).Table("withdraw_request").
		Where("DATE(created_at) = ?", date.Format("2006-01-02")).Select("withdraw_no, amount_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return money.IndexAmounts(rows, func(i int) (string, int64) { return rows[i].WithdrawNo, rows[i].AmountCents }), nil
}

// indexAmounts 把任意行切片按 pick 取出的“参照号 → 金额”汇总成 map，跳过空参照号，重复参照号累加。
func indexAmounts[T any](rows []T, pick func(int) (string, int64)) map[string]int64 {
	out := map[string]int64{}
	for i := range rows {
		ref, cents := pick(i)
		if ref != "" {
			out[ref] += cents
		}
	}
	return out
}
