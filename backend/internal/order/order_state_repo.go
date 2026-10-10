package order

import (
	"chargepilot/internal/domain/money"
	orderdomain "chargepilot/internal/domain/order"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// ChargingState 是停机决策需要的订单当前状态，不携带支付或卡会话模型。
type ChargingState struct {
	StartedAt time.Time               // 实际开始充电的 UTC 时刻；未开始时为零值
	Status    orderdomain.OrderStatus // 订单持久化状态，只有 charging 可继续请求自动停机
}

// LockChargingState 在调用方事务中锁定 id 对应的未删除订单并返回当前状态。
// 无订单返回 gorm.ErrRecordNotFound，其他错误表示读取失败；调用方负责提交或回滚。
// 刷卡路径必须先锁卡会话，再调用本函数；本函数不修改订单状态。
func LockChargingState(tx *gorm.DB, id uint64) (ChargingState, error) {
	var row ChargingState
	err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// PaymentStatusSQL 只供采用固定支付表别名 p 的内部查询使用，输出的取值与
// orderdomain.OrderPaymentStatus 一致；分支值是表达式的一部分，不单独参数化。
// 金额是到账与已成功退款的凭据；退款状态但退款金额为零不提前宣称已退款。
const PaymentStatusSQL = "CASE WHEN p.paid_cents>0 AND p.refunded_cents>=p.paid_cents THEN 'refunded' WHEN p.refunded_cents>0 THEN 'partial_refunded' WHEN p.paid_cents>0 OR p.status IN ('paid','partial_refunded','refunded') THEN 'paid' ELSE 'pending' END"

// SettledPaymentStatus 依据已经到账和已成功退款的金额计算展示状态。
// paidCents/refundedCents 为已到账、成功退款金额（分），ledgerStatus 是支付单状态；返回订单支付状态。
// 退款申请或渠道处理中不增加 refunded_cents，因此不会提前变成已退款。
func SettledPaymentStatus(paidCents, refundedCents int64, ledgerStatus money.PaymentStatus) orderdomain.OrderPaymentStatus {
	if paidCents > 0 && refundedCents >= paidCents {
		return orderdomain.PaymentRefunded
	}
	if refundedCents > 0 {
		return orderdomain.PaymentPartialRefunded
	}
	if paidCents > 0 || ledgerStatus == money.PayStatusPaid || ledgerStatus == money.PayStatusPartialRefunded || ledgerStatus == money.PayStatusRefunded {
		return orderdomain.PaymentPaid
	}
	return orderdomain.PaymentPending
}

// SyncOrderPaymentStatus 与实际支付/退款金额写入共用事务，不使用隐式 ORM hook。
// 一笔支付可能尚未生成充电订单（例如过期支付自动退款），也可能是钱包充值。
// 这两种情况没有订单资料需要修改。业务状态由数据库的 STORED generated 列维护。
// tx 为调用方资金事务，paymentID 为支付编号；返回读取、写入错误，不提交或修改支付表。
func SyncOrderPaymentStatus(tx *gorm.DB, paymentID uint64) error {
	var payment struct {
		ID            uint64                         // 支付主键
		BizType       string                         // charge 或 wallet_recharge
		UserID        uint64                         // 付款用户
		PaymentStatus orderdomain.OrderPaymentStatus // 实际到账与成功退款对应状态
	}
	if err := tx.Table("payment_order p").Select("p.id,p.biz_type,p.user_id,"+PaymentStatusSQL+" AS payment_status").Where("p.id=?", paymentID).Take(&payment).Error; err != nil {
		return err
	}
	if payment.BizType != "charge" {
		return nil
	}
	return tx.Table("charge_order").Where("payment_order_id=? AND user_id=?", payment.ID, payment.UserID).Update("payment_status", payment.PaymentStatus).Error
}

// SetStatusRefunded 在调用方事务中把订单状态设为 refunded。
// 仅用于起充失败且资金已退回的订单，申请退款不能提前标记完成。
func SetStatusRefunded(tx *gorm.DB, orderID uint64) error {
	return tx.Model(&ChargeOrderRecord{}).Where("id=?", orderID).Update("status", orderdomain.StatusRefunded).Error
}
