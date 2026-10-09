package settlement

import (
	orderstore "chargepilot/internal/order"
	pay "chargepilot/internal/payment"

	"chargepilot/internal/platform/dbutil"
	"chargepilot/internal/wallet"
	"errors"
	"gorm.io/gorm"
)

// ErrBalanceRefundConflict 表示余额退款超过该支付的剩余可退金额。
var ErrBalanceRefundConflict = errors.New("balance payment refund amount conflicts with payment")

// BalanceRefundSource 保存退款所需订单 ID、用户 ID 和支付 ID，无需传入完整订单模型。
type BalanceRefundSource struct {
	UserID         uint64 // 同一订单所有人，钱包入金与退款记录都归此人
	OrderID        uint64 // 充电订单主键，作为退款记录的业务关联
	PaymentOrderID int64  // 关联支付主键；必须指向该笔余额支付，未关联为零
}

// RefundBalanceInTx 在同一事务内退回余额，并更新支付、订单状态和退款记录。
// 调用方须先锁钱包，再锁支付；金额单位为分，非正金额直接返回。
// 金额冲突或写入失败须整体回滚；退款号必须保持不变。
func RefundBalanceInTx(tx *gorm.DB, src BalanceRefundSource, w *wallet.Row, cents int64, refundNo, reason string) error {
	if cents <= 0 {
		return nil
	}
	if err := wallet.Move(tx, src.UserID, w, cents, refundNo, reason); err != nil {
		return err
	}
	var payment pay.PaymentOrderRecord
	if err := tx.Where("id=? AND pay_method='balance'", src.PaymentOrderID).Take(&payment).Error; err != nil {
		return err
	}
	if payment.PaidCents-payment.RefundedCents < cents {
		return ErrBalanceRefundConflict
	}
	status := "partial_refunded"
	if payment.RefundedCents+cents == payment.PaidCents {
		status = "refunded"
	}
	if err := pay.AddPaymentRefunded(tx, payment.ID, cents, status); err != nil {
		return err
	}
	if err := orderstore.SyncOrderPaymentStatus(tx, payment.ID); err != nil {
		return err
	}
	return InsertRefundRecord(tx, RefundRecordInput{
		RefundNo: refundNo, PaymentOrderID: payment.ID, UserID: src.UserID,
		BizType: "charge", BizID: src.OrderID, RefundCents: cents, Status: "success",
		ExecutionPolicy: "automatic", Reason: reason,
	}, dbutil.MonthStart())
}
