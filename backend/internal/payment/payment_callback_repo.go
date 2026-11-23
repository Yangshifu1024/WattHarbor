package payment

import (
	"chargepilot/internal/domain/money"
	orderdomain "chargepilot/internal/domain/order"
	"chargepilot/internal/marketing"
	orderstore "chargepilot/internal/order"
	"chargepilot/internal/wallet"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"chargepilot/internal/ops"

	"chargepilot/internal/platform/dbutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrPaymentCallbackConflict 表示通知标识、金额或支付关联不一致，不能更新任何到账状态。
var ErrPaymentCallbackConflict = errors.New("verified payment does not match intent")

// VerifiedPayment 保存渠道确认的成功支付；调用方须先验签、解密或查询渠道。
type VerifiedPayment struct {
	Provider        string    // 已验签渠道，与装配渠道一致
	MerchantID      string    // 已验签收款商户，与本进程商户一致
	AppID           string    // 已验签应用，与支付应用一致
	MerchantOrderNo string    // 商户支付单号，最长 64 字节
	TransactionID   string    // 渠道交易唯一编号，最长 64 字节
	OpenID          string    // 已验签付款人，必须匹配申请
	PaidCents       int64     // 渠道实际支付金额，分，严格匹配原单
	PaidAt          time.Time // 渠道确认时刻，规范化到 UTC 毫秒
}

// PaymentCallbackResult 是到账业务回执，客户端收到成功不代表设备已经启动。
type PaymentCallbackResult struct {
	ChargeOrderID  uint64 `json:"charge_order_id,omitempty"` // 正常充电付款的订单主键，充值及迟到支付省略
	ChargeOrderNo  string `json:"charge_order_no,omitempty"` // 已冻结的充电订单编号
	RefundRequired bool   `json:"refund_required"`           // 迟到付款已登记原路退款，尚未代表渠道退回
	Replayed       bool   `json:"replayed"`                  // 相同付款人、交易号和金额已处理
}

// CallbackService 核对渠道交易，事务内更新支付、创建订单或充值钱包。
type CallbackService struct {
	NotifyChargePaid   func()             // 成功提交可启动订单后通知同进程派发器。
	DB                 *gorm.DB           // central 支付连接，由进程管理
	ExpectedProvider   string             // 本进程接受的渠道
	ExpectedMerchantID string             // 本进程收款商户
	ExpectedAppID      string             // 本进程支付应用
	Refunds            LateRefundRecorder // 同事务迟到退款及 Outbox 能力，cmd 注入 settlement 实现
}

// entryRejected 在事务前校验已验证通知的配置、标识、金额与时间；不合格返回 true。
func (s CallbackService) entryRejected(payment VerifiedPayment, paidAt, now time.Time) bool {
	return s.DB == nil || s.ExpectedProvider == "" || s.ExpectedMerchantID == "" || s.ExpectedAppID == "" ||
		payment.Provider != s.ExpectedProvider || payment.MerchantID != s.ExpectedMerchantID || payment.AppID != s.ExpectedAppID ||
		payment.MerchantOrderNo == "" || len(payment.MerchantOrderNo) > 64 ||
		payment.TransactionID == "" || len(payment.TransactionID) > 64 || payment.OpenID == "" ||
		payment.PaidCents <= 0 || paidAt.IsZero() || paidAt.After(now.Add(5*time.Minute))
}

// Apply 按 payment 商户单号锁定支付，核对标识、金额、关联及交易摘要。
// 返回首次或重放回执；任一存储、权益、退款或建单错误回滚整个到账事务。
// READ COMMITTED 下正常充电的意图、价格快照、订单、优惠核销及 Outbox 同时提交。
// 充值同事务调用钱包到账，迟到支付仅登记退款，不创建可启动的充电订单。
func (s CallbackService) Apply(ctx context.Context, payment VerifiedPayment) (PaymentCallbackResult, error) {
	paidAt := payment.PaidAt.UTC().Truncate(time.Millisecond)
	if s.entryRejected(payment, paidAt, time.Now()) {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}

	digest := callbackDigest(payment)
	var callbackResult PaymentCallbackResult
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先按渠道商户单号定位支付订单，再按业务类型分流；钱包充值不创建支付意图。
		var order PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", payment.MerchantOrderNo).Take(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentCallbackConflict
			}
			return err
		}
		if order.BizType == money.PaymentBizWalletRecharge {
			return settleWalletRecharge(tx, order, payment, digest, paidAt, &callbackResult)
		}
		if order.BizType != money.PaymentBizCharge {
			return ErrPaymentCallbackConflict
		}
		var intent PaymentIntentRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("merchant_order_no = ?", payment.MerchantOrderNo).Take(&intent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPaymentCallbackConflict
		}
		if err != nil {
			return err
		}
		// 支付申请必须关联本支付单，随后核对用户、付款人和金额。
		if intent.PaymentOrderID != order.ID {
			return ErrPaymentCallbackConflict
		}
		if order.UserID != intent.UserID || intent.OpenID != payment.OpenID || order.TotalCents != intent.TotalCents || order.TotalCents != payment.PaidCents ||
			intent.ChargeQuantity == 0 || intent.PortNo == 0 || order.ID > math.MaxInt64 {
			return ErrPaymentCallbackConflict
		}

		var previousDigest PaymentCallbackDigestRecord
		digestErr := tx.Where("wechat_transaction_id = ?", payment.TransactionID).Take(&previousDigest).Error
		if digestErr != nil && !errors.Is(digestErr, gorm.ErrRecordNotFound) {
			return digestErr
		}
		if digestErr == nil && previousDigest.RequestDigest != digest {
			return ErrPaymentCallbackConflict
		}

		if order.Status == money.PayStatusPaid || order.Status == money.PayStatusPartialRefunded || order.Status == money.PayStatusRefunded {
			if digestErr != nil || !order.WechatTransactionID.Valid || order.WechatTransactionID.String != payment.TransactionID || order.PaidCents != payment.PaidCents {
				return ErrPaymentCallbackConflict
			}
			if intent.Status == money.IntentRefundRequired && !intent.ChargeOrderID.Valid {
				callbackResult = PaymentCallbackResult{RefundRequired: true, Replayed: true}
				return nil
			}
			if intent.Status != money.IntentPaid || !intent.ChargeOrderID.Valid || intent.ChargeOrderID.Int64 <= 0 || order.BizID != uint64(intent.ChargeOrderID.Int64) {
				return ErrPaymentCallbackConflict
			}
			var chargeOrder orderstore.ChargeOrderRecord
			if err := tx.Select("order_no").Where("id = ? AND payment_order_id = ?", order.BizID, order.ID).Take(&chargeOrder).Error; err != nil {
				return err
			}
			callbackResult = PaymentCallbackResult{ChargeOrderID: order.BizID, ChargeOrderNo: chargeOrder.OrderNo, Replayed: true}
			return nil
		}
		if order.Status != money.PayStatusInitiated || order.BizID != 0 || digestErr == nil {
			return ErrPaymentCallbackConflict
		}
		if err := tx.Create(&PaymentCallbackDigestRecord{WechatTransactionID: payment.TransactionID, RequestDigest: digest}).Error; err != nil {
			if dbutil.IsMySQLDuplicate(err) {
				return ErrPaymentCallbackConflict
			}
			return err
		}

		updated := tx.Model(&PaymentOrderRecord{}).Where("id = ? AND order_no = ? AND status = ? AND total_cents = ? AND paid_cents = 0", order.ID, order.OrderNo, money.PayStatusInitiated, payment.PaidCents).
			Updates(map[string]any{"status": money.PayStatusPaid, "paid_cents": payment.PaidCents, "wechat_transaction_id": payment.TransactionID, "paid_at": paidAt})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrPaymentCallbackConflict
		}

		if intent.Status != money.IntentInitiated || time.Now().After(intent.ExpiresAt) || paidAt.After(intent.ExpiresAt) {
			if err := s.queueLatePaymentRefund(tx, intent, order, payment, digest, paidAt); err != nil {
				return err
			}
			callbackResult = PaymentCallbackResult{RefundRequired: true}
			return nil
		}

		// 支付确认到账后才核销优惠券。
		if intent.CouponGrantID != 0 {
			if err := (marketing.CouponService{}).RedeemInTx(tx, marketing.RedeemRef{
				UserID:        intent.UserID,
				CouponGrantID: intent.CouponGrantID,
				TotalCents:    intent.TotalCents,
				DiscountCents: intent.DiscountCents,
				PaymentOrder:  order.ID,
			}, pendingChargeNoFor(order.ID)); err != nil {
				return err
			}
		}

		chargeNo, err := orderstore.NewChargeOrderNumber(tx, intent.DeviceID, intent.PortNo)
		if err != nil {
			return err
		}
		chargeOrder := orderstore.ChargeOrderRecord{OrderNo: chargeNo, UserID: intent.UserID, DeviceID: intent.DeviceID, PortNo: intent.PortNo,
			PortCode: sql.NullString{String: intent.PortCode, Valid: true}, PaymentOrderID: sql.NullInt64{Int64: int64(order.ID), Valid: true},
			Status: orderdomain.StatusPaid, PaymentStatus: orderdomain.PaymentPaid, ChargeMode: intent.ChargeMode, ChargeQuantity: intent.ChargeQuantity,
			DiscountCents: intent.DiscountCents, CreatedMonth: dbutil.MonthStart()}
		if err := orderstore.CreatePaidInTx(tx, &chargeOrder); err != nil {
			return err
		}
		if chargeOrder.ID == 0 || chargeOrder.ID > math.MaxInt64 {
			return ErrPaymentCallbackConflict
		}
		if err := orderstore.FreezePricingInTx(tx, orderstore.ChargePricingSnapshotRecord{ChargeOrderID: chargeOrder.ID, PaymentIntentID: intent.IntentID,
			UserID: intent.UserID, PortCode: intent.PortCode, PricingSnapshot: intent.PricingSnapshot}); err != nil {
			return err
		}
		bound := tx.Model(&PaymentOrderRecord{}).Where("id = ? AND biz_id = 0 AND status = ?", order.ID, money.PayStatusPaid).Update("biz_id", chargeOrder.ID)
		if bound.Error != nil {
			return bound.Error
		}
		if bound.RowsAffected != 1 {
			return ErrPaymentCallbackConflict
		}
		paid := tx.Model(&PaymentIntentRecord{}).Where("intent_id = ? AND status = ? AND charge_order_id IS NULL", intent.IntentID, money.IntentInitiated).
			Updates(map[string]any{"status": money.IntentPaid, "paid_at": paidAt, "charge_order_id": chargeOrder.ID})
		if paid.Error != nil {
			return paid.Error
		}
		if paid.RowsAffected != 1 {
			return ErrPaymentCallbackConflict
		}

		eventID := "P" + digest[:32]
		if err := orderstore.AppendEventInTx(tx, orderstore.ChargeEventLogRecord{ChargeOrderID: chargeOrder.ID, EventID: eventID,
			Event: "payment_confirmed", Actor: "payment_callback", Detail: "verified payment callback", OccurredAt: paidAt}); err != nil {
			return err
		}
		envelope, err := json.Marshal(map[string]any{"event_id": eventID, "charge_order_id": chargeOrder.ID, "order_no": chargeNo, "payment_order_id": order.ID})
		if err != nil {
			return err
		}
		if err := ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: eventID, Stream: "charge_payment_confirmed_stream", EnvelopeJSON: envelope}); err != nil {
			return err
		}
		callbackResult = PaymentCallbackResult{ChargeOrderID: chargeOrder.ID, ChargeOrderNo: chargeNo}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err == nil && callbackResult.ChargeOrderID != 0 && s.NotifyChargePaid != nil {
		s.NotifyChargePaid()
	}
	return callbackResult, err
}

// queueLatePaymentRefund 在到账事务中登记迟到支付的自动退款与 Outbox。
// 退款编号由通知摘要稳定派生，失败回滚支付及摘要；只登记退款，不调用渠道。
func (s CallbackService) queueLatePaymentRefund(tx *gorm.DB, intent PaymentIntentRecord, order PaymentOrderRecord, payment VerifiedPayment, digest string, paidAt time.Time) error {
	changed := tx.Model(&PaymentIntentRecord{}).Where("intent_id = ? AND status IN ?", intent.IntentID, refundableIntentStatuses()).
		Updates(map[string]any{"status": money.IntentRefundRequired, "paid_at": paidAt})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return ErrPaymentCallbackConflict
	}
	if s.Refunds == nil {
		return errors.New("late refund recorder not configured")
	}
	return s.Refunds.RecordLateInTx(tx, money.RefundDraft{RefundNo: "REF" + digest[:32], PaymentOrderID: order.ID, UserID: intent.UserID, BizType: "charge", AmountCents: payment.PaidCents, Reason: "payment arrived after intent expired", CreatedMonth: dbutil.MonthStart()}, "R"+digest[:32])
}

// refundableIntentStatuses 返回可被迟到到账改写为需退款的意图状态集合。
// 仅 initiated 与 expired 两种意图可转 refund_required；取值来自 money.PaymentIntentStatus 词表。
func refundableIntentStatuses() []money.PaymentIntentStatus {
	return []money.PaymentIntentStatus{money.IntentInitiated, money.IntentExpired}
}

// callbackDigest 对 payment 的渠道、商户、应用、付款人、原单、交易号及金额（分）生成 SHA256 十六进制摘要。
// 不含到账时刻，容许同一交易重新查询时刻不同；金额或身份变化不能重放。
func callbackDigest(payment VerifiedPayment) string {
	value := payment.Provider + "\x00" + payment.MerchantID + "\x00" + payment.AppID + "\x00" + payment.OpenID + "\x00" + payment.MerchantOrderNo + "\x00" + payment.TransactionID + "\x00" +
		decimalAmount(payment.PaidCents)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// decimalAmount 将金额（分）转为稳定的十进制文本，供通知摘要使用。
func decimalAmount(amount int64) string { return strconv.FormatInt(amount, 10) }

// settleWalletRecharge 对验签通过且金额匹配的渠道充值入账。
// 钱包冻结或其他不可用状态不阻止资金记账；可用性限制仅影响后续消费。
// tx 已锁 order 支付单；payment 是渠道交易，digest 是通知摘要，paidAt 是 UTC 到账时刻。
// 成功回填 result；标识、金额、摘要、权益或写入错误回滚支付、钱包、奖励及 Outbox，不自行提交。
func settleWalletRecharge(tx *gorm.DB, order PaymentOrderRecord, payment VerifiedPayment, digest string, paidAt time.Time, result *PaymentCallbackResult) error {
	request, err := wallet.LockRechargeForPayment(tx, order.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrPaymentCallbackConflict
	}
	if err != nil {
		return err
	}
	// 申请人、付款人和金额必须与支付单及通知一致。
	if request.UserID != order.UserID || request.OpenID == "" || request.OpenID != payment.OpenID || request.AmountCents != payment.PaidCents || order.TotalCents != payment.PaidCents {
		return ErrPaymentCallbackConflict
	}

	var previousDigest PaymentCallbackDigestRecord
	digestErr := tx.Where("wechat_transaction_id = ?", payment.TransactionID).Take(&previousDigest).Error
	if digestErr != nil && !errors.Is(digestErr, gorm.ErrRecordNotFound) {
		return digestErr
	}
	if digestErr == nil && previousDigest.RequestDigest != digest {
		return ErrPaymentCallbackConflict
	}
	// 已处理的支付单只核对交易号、金额和通知摘要，不再次充值。
	// 重放必须匹配渠道交易、金额和验签载荷摘要，不再次增加钱包余额。
	if order.Status == money.PayStatusPaid || order.Status == money.PayStatusPartialRefunded || order.Status == money.PayStatusRefunded {
		if digestErr != nil || !order.WechatTransactionID.Valid ||
			order.WechatTransactionID.String != payment.TransactionID || order.PaidCents != payment.PaidCents {
			return ErrPaymentCallbackConflict
		}
		*result = PaymentCallbackResult{Replayed: true}
		return nil
	}
	if order.Status != money.PayStatusInitiated || order.BizID != 0 || digestErr == nil {
		return ErrPaymentCallbackConflict
	}
	if err := tx.Create(&PaymentCallbackDigestRecord{WechatTransactionID: payment.TransactionID, RequestDigest: digest}).Error; err != nil {
		if dbutil.IsMySQLDuplicate(err) {
			return ErrPaymentCallbackConflict
		}
		return err
	}
	paid := tx.Model(&PaymentOrderRecord{}).
		Where("id = ? AND order_no = ? AND status = ? AND total_cents = ? AND paid_cents = 0",
			order.ID, order.OrderNo, money.PayStatusInitiated, payment.PaidCents).
		Updates(map[string]any{"status": money.PayStatusPaid, "paid_cents": payment.PaidCents,
			"wechat_transaction_id": payment.TransactionID, "paid_at": paidAt})
	if paid.Error != nil {
		return paid.Error
	}
	if paid.RowsAffected != 1 {
		return ErrPaymentCallbackConflict
	}

	if err := wallet.CreditRechargeInTx(tx, order.UserID, request.RequestID, digest, payment.PaidCents); err != nil {
		return err
	}
	// 在充值事务内求值首充奖励；事务回滚时奖励一并回滚。规则不适用时跳过，不阻止结算。
	if _, err := (marketing.ActivityService{}).ApplyInTx(tx, marketing.ActivityEvent{
		TriggerType: marketing.TriggerFirstRecharge,
		UserID:      order.UserID,
		EventKey:    marketing.ActivityEventKeyForRecharge(request.RequestID),
		AmountCents: payment.PaidCents,
		Now:         paidAt,
	}); err != nil && !errors.Is(err, marketing.ErrActivityNotApplicable) {
		return err
	}

	eventID := "W" + digest[:32]
	envelope, err := json.Marshal(map[string]any{"event_id": eventID, "user_id": order.UserID,
		"payment_order_id": order.ID, "recharge_request_id": request.RequestID, "amount_cents": payment.PaidCents})
	if err != nil {
		return err
	}
	if err := ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: eventID, Stream: "wallet_recharge_settled_stream", EnvelopeJSON: envelope}); err != nil {
		return err
	}
	*result = PaymentCallbackResult{}
	return nil
}

// pendingChargeNoFor 在充电行还不存在时推导出核销单号。
// paymentOrderID 为支付主键，返回稳定 PENDING- 编号。
func pendingChargeNoFor(paymentOrderID uint64) string {
	return fmt.Sprintf("PENDING-%d", paymentOrderID)
}
