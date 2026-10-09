package order

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockInvoiceSourceInTx 在事务中锁定 userID 所属、未删除的 id 订单，返回当前生命周期状态。
// settlement 以此串行同订单开票并重新核对结算校验；不写状态、不提交事务，不能锁他人订单。
func LockInvoiceSourceInTx(tx *gorm.DB, id, userID uint64) (string, error) {
	var row struct {
		Status string // 订单当前生命周期状态
	}
	err := tx.Model(&ChargeOrderRecord{}).Select("status").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND deleted_at IS NULL", id, userID).Take(&row).Error
	return row.Status, err
}

// CreatePaidInTx 在调用方事务中写入已确认到账的 in 订单，并回填分配的主键。
// 付款标识、金额及设备校验由 payment 核对；错误要求整个回调事务回滚，不自行提交。
func CreatePaidInTx(tx *gorm.DB, in *ChargeOrderRecord) error { return tx.Create(in).Error }

// FreezePricingInTx 在事务中首写 in 的下单方案快照，不读取当前发布方案。
// 唯一键或存储失败返回错误，调用方与支付、订单及 Outbox 原子提交。
func FreezePricingInTx(tx *gorm.DB, in ChargePricingSnapshotRecord) error {
	return tx.Create(&in).Error
}

// AppendEventInTx 在事务中保存 in 的订单生命周期凭据；事件编号必须沿用业务请求。
// 错误要求调用方回滚状态及资金，函数不提交事务或发布网络事件。
func AppendEventInTx(tx *gorm.DB, in ChargeEventLogRecord) error { return tx.Create(&in).Error }

// SetSettledFeeInTx 在已锁订单的事务中为 id 保存 electric、service、total（单位分）。
// 费用回执已由 settlement 核对，错误不提交任何费用或状态，不改变计价快照。
func SetSettledFeeInTx(tx *gorm.DB, id uint64, electric, service, total int64) error {
	return tx.Model(&ChargeOrderRecord{}).Where("id=?", id).Updates(map[string]any{"electric_cents": electric, "service_cents": service, "total_cents": total}).Error
}

// RequestChannelRefundInTx 在事务中仅将 id 的 completed 订单置为 refunding。
// 本动作表示待渠道退款，不表示已到账；返回数据库错误，不提交调用方事务。
func RequestChannelRefundInTx(tx *gorm.DB, id uint64) error {
	return tx.Model(&ChargeOrderRecord{}).Where("id=? AND status='completed'", id).Update("status", "refunding").Error
}

// CompleteChannelRefundInTx 在资金回执 tx 中更新 id、paymentID 匹配的 refunding 订单。
// fully 表示全额已确认退回；部分退回且无在途退款时仅恢复已结束订单为 completed。
// 错误要求资金、回执及订单一起回滚，不改变不匹配或其他状态的订单。
func CompleteChannelRefundInTx(tx *gorm.DB, id, paymentID uint64, fully bool) error {
	query := tx.Model(&ChargeOrderRecord{}).Where("id=? AND payment_order_id=? AND status='refunding'", id, paymentID)
	if fully {
		return query.Update("status", "refunded").Error
	}
	return query.Where("ended_at IS NOT NULL").Update("status", "completed").Error
}
