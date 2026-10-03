package card

import (
	"database/sql"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	pay "github.com/ChargePilot2026/charge-pilot/internal/payment"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"gorm.io/gorm"
)

// RefundInTx 是刷卡退款的跨家族编排：钱包入金、payment_order 改写、
// 订单支付状态同步与退款记录创建在同一事务内完成。
// 由结算（刷卡剩余退款）与启动失败退款持有 tx 调用；钱包行须已由调用方按
// 既有加锁顺序（先钱包后支付）锁定。
func RefundInTx(tx *gorm.DB, order orderstore.ChargeOrderRecord, w *wallet.Row, cents int64, refundNo, reason string) error {
	if cents <= 0 {
		return nil
	}
	if err := mapWalletMoveError(wallet.Move(tx, order.UserID, w, cents, refundNo, reason)); err != nil {
		return err
	}
	var payment pay.PaymentOrderRecord
	if err := tx.Where("id=? AND pay_method='balance'", order.PaymentOrderID.Int64).Take(&payment).Error; err != nil {
		return err
	}
	if payment.PaidCents-payment.RefundedCents < cents {
		return ErrCardOperation
	}
	status := "partial_refunded"
	if payment.RefundedCents+cents == payment.PaidCents {
		status = "refunded"
	}
	if err := tx.Model(&pay.PaymentOrderRecord{}).Where("id=?", payment.ID).Updates(map[string]any{"refunded_cents": gorm.Expr("refunded_cents+?", cents), "status": status}).Error; err != nil {
		return err
	}
	if err := orderstore.SyncOrderPaymentStatus(tx, payment.ID); err != nil {
		return err
	}
	return tx.Create(&pay.RefundRecord{RefundNo: refundNo, PaymentOrderID: payment.ID, UserID: order.UserID, BizType: "charge", BizID: order.ID, RefundCents: cents, Status: "success", ExecutionPolicy: "automatic", Reason: sql.NullString{String: reason, Valid: true}, CreatedMonth: dbutil.MonthStart()}).Error
}
