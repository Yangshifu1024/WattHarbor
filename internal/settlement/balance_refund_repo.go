package settlement

import (
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	pay "github.com/ChargePilot2026/charge-pilot/internal/payment"

	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"gorm.io/gorm"
)

// ErrBalanceRefundConflict 表示本次余额退款超过原支付的未退金额。
var ErrBalanceRefundConflict = errors.New("balance payment refund amount conflicts with payment")

// BalanceRefundSource 是余额退款所需的原订单身份，由调用方按其自有读模型提供。
// 退款只依赖订单主键、所有人与原支付主键三项事实，调用方无需把订单 ORM 模型交给结算。
type BalanceRefundSource struct {
	UserID         uint64 // 原订单所有人，钱包入金与退款记录都归此人
	OrderID        uint64 // 原充电订单主键，作为退款记录的业务关联
	PaymentOrderID int64  // 原关联支付主键；必须指向该笔余额支付，未关联为零
}

// RefundBalanceInTx 在已有业务事务中处理余额支付退款：钱包入金、payment_order 改写、
// 订单支付状态同步与退款记录创建在同一事务内完成。
// 由结算（刷卡剩余退款）与启动失败退款持有 tx 调用；钱包行须已由调用方按
// 既有加锁顺序（先钱包后支付）锁定。
// src 提供原订单与支付身份，w 为已锁钱包，cents 为退款分数，refundNo 为稳定退款号，reason 为流水原因。
// cents 非正时返回 nil 且无 IO；金额冲突及存储错误要求调用方整体回滚，成功不自行提交。
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
