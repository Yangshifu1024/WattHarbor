package stop

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CutoffRepo 在调用方事务中检查订单是否已有计费截止记录。
type CutoffRepo struct{}

// FrozenInTx 在调用方事务中检查 orderID 是否已有截止记录。
// 调用方须已锁订单；SHARE 读取最新已提交记录，避免 RR 快照漏掉其他事务刚保存的截止；无行返回 false。
// 不写入、延长截止或提交事务，不返回截止费用以供消费者重新计价。
func (CutoffRepo) FrozenInTx(tx *gorm.DB, orderID uint64) (bool, error) {
	var row struct {
		ChargeOrderID uint64 /* 截止唯一订单编号 */
	}
	err := tx.Table("charge_billing_cutoff").Clauses(clause.Locking{Strength: "SHARE"}).Where("charge_order_id=?", orderID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// RecordBillingCutoff 保存首次计费截止时刻，结算时按该时刻截取可用计量。
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

// FindBillingCutoff 使用 db 查询 chargeOrderID 的截止记录。
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

// FreezeBillingCutoff 在事务中按 orderID 首写截止记录。
// row 包含截止 UTC 时刻、原因和可选费用快照（分）；调用方须已锁订单且负责提交。
// 唯一键保证每单一条记录；已有记录不更新时刻、原因或费用，写入失败返回错误。
func FreezeBillingCutoff(tx *gorm.DB, orderID uint64, row map[string]any) error {
	row["charge_order_id"] = orderID
	return tx.Table("charge_billing_cutoff").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(row).Error
}
