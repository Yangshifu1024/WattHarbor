package stop

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordBillingCutoff 记下计费截止时刻：停机之后发生的电量不再计入本单。
// 首次写入冻结截止点；重复请求使用 no-op upsert，不延长 cutoff_at。
func RecordBillingCutoff(ctx context.Context, db *gorm.DB, chargeOrderID uint64, reason string, at time.Time) error {
	return db.WithContext(ctx).Table("charge_billing_cutoff").
		Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).
		Create(map[string]any{"charge_order_id": chargeOrderID, "cutoff_at": at, "reason": reason}).Error
}

// BillingCutoff 是一笔订单的计费截止记录。取不到表示还没截止。
// cutoff_at 按文本返回；缺失记录为 nil，时刻解析由调用方处理。
type BillingCutoff struct {
	CutoffAt *string `json:"cutoff_at" gorm:"column:cutoff_at"`
	Reason   string  `json:"reason" gorm:"column:reason"`
}

// FindBillingCutoff 返回订单的计费截止时刻。
func FindBillingCutoff(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingCutoff, error) {
	var row BillingCutoff
	err := db.WithContext(ctx).Table("charge_billing_cutoff").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}

// BillingCutoffFrozen 数出该订单已有的计费截止记录数。大于 0 表示已经冻结过。
func BillingCutoffFrozen(ctx context.Context, db *gorm.DB, orderID uint64) (int64, error) {
	var frozen int64
	err := db.WithContext(ctx).Table("charge_billing_cutoff").
		Where("charge_order_id=?", orderID).Count(&frozen).Error
	return frozen, err
}

// FreezeBillingCutoff 为订单首写计费截止点。调用方需先用 BillingCutoffFrozen
// 确认尚未冻结——这里不做判断，因为"是否重复"是业务判断，不是存储细节。
// row 带上截止时刻、原因与计量快照，由调用方组装。
func FreezeBillingCutoff(tx *gorm.DB, orderID uint64, row map[string]any) error {
	return tx.Table("charge_billing_cutoff").Create(row).Error
}
