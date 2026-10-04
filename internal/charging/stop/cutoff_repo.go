package stop

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordBillingCutoff 记下计费截止时刻：停机之后发生的电量不再计入本单。
// ctx 控制取消，db 可为事务，chargeOrderID 为订单主键，reason 为业务原因，at 为 UTC 截止时刻。
// 返回数据库错误；不修改订单状态，不提交调用方事务。
// 首次写入冻结截止点；重复请求使用 no-op upsert，不延长 cutoff_at。
func RecordBillingCutoff(ctx context.Context, db *gorm.DB, chargeOrderID uint64, reason string, at time.Time) error {
	return db.WithContext(ctx).Table("charge_billing_cutoff").
		Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).
		Create(map[string]any{"charge_order_id": chargeOrderID, "cutoff_at": at, "reason": reason}).Error
}

// BillingCutoff 是一笔订单的计费截止记录。取不到表示还没截止。
// cutoff_at 按文本返回；缺失记录为 nil，时刻解析由调用方处理。
type BillingCutoff struct {
	CutoffAt *string `json:"cutoff_at" gorm:"column:cutoff_at"` // 数据库格式的首次截止时刻文本，nil 表示未冻结
	Reason   string  `json:"reason" gorm:"column:reason"`       // 首次冻结原因，重试不覆盖
}

// FindBillingCutoff 在 ctx 下使用 db 查询 chargeOrderID 的截止记录。
// 无记录返回零值及 nil；数据库读取失败返回错误，不写入数据。
func FindBillingCutoff(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingCutoff, error) {
	var row BillingCutoff
	err := db.WithContext(ctx).Table("charge_billing_cutoff").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}

// BillingCutoffFrozen 使用 ctx 和 db 统计 orderID 的截止记录。
// 返回 0 表示未冻结，1 表示已冻结；错误表示读取失败，不作停机依据。
func BillingCutoffFrozen(ctx context.Context, db *gorm.DB, orderID uint64) (int64, error) {
	var frozen int64
	err := db.WithContext(ctx).Table("charge_billing_cutoff").
		Where("charge_order_id=?", orderID).Count(&frozen).Error
	return frozen, err
}

// FreezeBillingCutoff 在 tx 内按 orderID 首写截止记录。
// row 包含截止 UTC 时刻、原因和可选费用快照（分）；调用方须已锁订单且负责提交。
// 并发唯一键由原子 no-op upsert 仲裁，已有时刻、原因和费用保持不变；错误表示写入失败。
func FreezeBillingCutoff(tx *gorm.DB, orderID uint64, row map[string]any) error {
	row["charge_order_id"] = orderID
	return tx.Table("charge_billing_cutoff").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(row).Error
}
