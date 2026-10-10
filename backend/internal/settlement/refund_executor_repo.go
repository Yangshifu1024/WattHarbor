package settlement

import (
	"chargepilot/internal/domain/money"
	"chargepilot/internal/ops"
	orderstore "chargepilot/internal/order"
	pay "chargepilot/internal/payment"
	"chargepilot/internal/wallet"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrRefundConflict 表示退款单号、金额、渠道、状态或可退额度与已保存记录不一致。
var ErrRefundConflict = errors.New("refund identity or amount conflict")

// ErrRefundReferenceMissing 表示支付、钱包分摊或钱包账户缺失；重新请求渠道无法补齐，退款应标为失败并停止重试。
var ErrRefundReferenceMissing = errors.New("refund references a record that no longer exists")

// walletSettlement 将退款单号和金额（分）转换为钱包结算输入。
func walletSettlement(r RefundRecord) wallet.Settlement {
	return wallet.Settlement{ID: r.ID, UserID: r.UserID, RefundNo: r.RefundNo, BizType: r.BizType, RefundCents: r.RefundCents}
}

// RefundExecutor 只派发明确属于自动充电或钱包充值退款的请求。
// 结果未知的退款保持 processing 状态，每次幂等重试之前都先查询一次。
type RefundExecutor struct {
	DB           *gorm.DB           // central 退款连接，由进程管理
	Provider     pay.RefundProvider // 渠道查询及创建退款能力，未知结果先查单
	ProviderName string             // 当前渠道标签，必须与支付渠道记录一致
}

// Batch 每批执行最多 20 笔到期自动退款，返回成功处理数和首个错误。
// 未配置渠道时返回 0、nil；取消后停止后续处理，已成功项不回滚。
func (e RefundExecutor) Batch(ctx context.Context) (int, error) {
	if e.Provider == nil {
		return 0, nil
	}
	var rows []RefundRecord
	// 字面量 'automatic' 对应 refund_record.execution_policy 的自动执行取值，
	// 属该列词表 money.ExecutionPolicy；与其余条件同处一个 Where 字面量串，
	// 拆开会改变 SQL 形状，故保留并在此指向类型。
	if err := e.DB.WithContext(ctx).Where("execution_policy='automatic' AND biz_type IN ? AND status IN ? AND next_attempt_at <= UTC_TIMESTAMP(3) AND deleted_at IS NULL", paymentBizTypes(), unsettledRefundStatuses()).Order("next_attempt_at,id").Limit(20).Find(&rows).Error; err != nil {
		return 0, err
	}
	count := 0
	var first error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if err := e.Execute(ctx, row.ID); err != nil {
			if first == nil {
				first = err
			}
		} else {
			count++
		}
	}
	return count, first
}

// Execute 先领取退款重试窗口并提交，再以十秒超时查询渠道，缺单才创建。
// 结果未知时保留 processing、首次单号和钱包冻结，下次先查询；成功回执在资金事务内入账。
// 未到期或已成功直接返回，前置记录缺失则标为失败。
func (e RefundExecutor) Execute(ctx context.Context, id uint64) error {
	if e.Provider == nil || e.ProviderName == "" {
		return errors.New("refund provider unavailable")
	}
	r, request, claimed, err := e.claim(ctx, id)
	if err != nil {
		// 依赖记录均先于退款创建；缺失时标为永久失败，避免每十秒重复扫描。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if failErr := e.failPermanent(ctx, id, "退款依赖的支付单或钱包记录已不存在"); failErr != nil {
				return failErr
			}
			return fmt.Errorf("%w: refund %d", ErrRefundReferenceMissing, id)
		}
		return err
	}
	if !claimed {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := e.Provider.QueryRefund(callCtx, request)
	if errors.Is(err, pay.ErrRefundNotFound) {
		result, err = e.Provider.CreateRefund(callCtx, request)
	}
	if err != nil {
		// 只落库脱敏后的诊断信息，绝不落 SDK 响应体或凭据。
		diagnosticErr := e.DB.WithContext(ctx).Model(&RefundRecord{}).
			Where("id = ? AND status=?", id, money.RefundProcessing).
			Update("failure_reason", "provider result unknown; query scheduled").Error
		unknown := fmt.Errorf("refund %s: provider result unknown", r.RefundNo)
		if diagnosticErr != nil {
			return errors.Join(unknown, fmt.Errorf("persist refund diagnostic: %w", diagnosticErr))
		}
		return unknown
	}
	return e.apply(ctx, r, request, result)
}

// failPermanent 将退款标为终态失败，保存脱敏原因，不再安排重试。
func (e RefundExecutor) failPermanent(ctx context.Context, id uint64, reason string) error {
	return e.DB.WithContext(ctx).Model(&RefundRecord{}).
		Where("id = ? AND status IN ?", id, unsettledRefundStatuses()).
		Updates(map[string]any{"status": money.RefundFailed, "failure_reason": reason}).Error
}

// claim 按钱包（适用时）→支付→退款加锁，核对用户、业务关联、金额和渠道。
// 领取时保存 processing、尝试次数和有界退避时间，然后提交；持锁期间不请求渠道。
// 未领取时不能发新请求，错误整体回滚，首次单号保持不变。
func (e RefundExecutor) claim(ctx context.Context, id uint64) (RefundRecord, pay.RefundRequest, bool, error) {
	var r RefundRecord
	var request pay.RefundRequest
	claimed := false
	err := e.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先在支付行上串行化金额，再去锁单条请求。
		if err := tx.Where("id = ? AND deleted_at IS NULL", id).Take(&r).Error; err != nil {
			return err
		}
		if err := wallet.LockRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		// payment 提供支付锁读方法，settlement 单向调用 payment。
		var p pay.RefundPayment
		var paymentErr error
		p, paymentErr = pay.LockPaymentForRefund(tx, r.PaymentOrderID)
		if err := paymentErr; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&r).Error; err != nil {
			return err
		}
		if r.Status == money.RefundSuccess {
			return nil
		}
		if r.ExecutionPolicy != money.ExecutionAutomatic || (!r.BizType.Valid()) || (!oneOf(r.Status, money.UnsettledRefundStatuses)) {
			return ErrRefundConflict
		}
		if r.NextAttemptAt.After(time.Now().UTC()) {
			return nil
		}
		if p.UserID != r.UserID || p.BizID != r.BizID || p.BizType != r.BizType || p.PayMethod != "wechat" || !p.WechatTransactionID.Valid || p.WechatTransactionID.String == "" || p.PaidCents != p.TotalCents || p.RefundedCents < 0 || r.RefundCents <= 0 || r.RefundCents > p.PaidCents-p.RefundedCents || (!oneOf(p.Status, money.RefundablePaymentStatuses)) {
			return ErrRefundConflict
		}
		if r.BizType == money.PaymentBizCharge {
			var prepay struct {
				ParamsJSON []byte // 预支付渠道参数，核对渠道身份
			}
			if err := tx.Table("charge_prepay").Where("payment_order_id = ?", p.ID).Take(&prepay).Error; err != nil {
				return err
			}
			var params pay.PrepayParams
			if json.Unmarshal(prepay.ParamsJSON, &params) != nil || params.Provider != e.ProviderName {
				return ErrRefundConflict
			}
		} else {
			if !providerMatchesChannel(e.ProviderName, p.WechatTransactionID.String) {
				return ErrRefundConflict
			}
		}

		var reserved int64
		// 支付锁不会刷新已有快照；在途退款须用锁读查看并发提交的最新预占。
		if err := tx.Model(&RefundRecord{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(refund_cents),0)").
			Where("payment_order_id = ? AND id <> ? AND status IN ? AND deleted_at IS NULL", p.ID, r.ID, unsettledRefundStatuses()).
			Scan(&reserved).Error; err != nil {
			return err
		}
		if reserved > p.PaidCents-p.RefundedCents-r.RefundCents {
			return ErrRefundConflict
		}
		request = pay.RefundRequest{RefundNo: r.RefundNo, MerchantOrderNo: p.OrderNo, TransactionID: p.WechatTransactionID.String, TotalCents: p.TotalCents, RefundCents: r.RefundCents}
		// 下次执行延后 30×2^n 秒，n 最大为 7；领取后崩溃仍沿用保存的重试时间。
		delay := time.Duration(30*(1<<min(r.RetryCount, uint32(7)))) * time.Second
		update := tx.Model(&RefundRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": money.RefundProcessing, "retry_count": gorm.Expr("retry_count+1"), "next_attempt_at": time.Now().UTC().Add(delay)})
		if update.Error != nil {
			return update.Error
		}
		claimed = true
		return nil
	})
	return r, request, claimed, err
}

// receiptMatches 严格比对请求与渠道回执的交易号、退款单号和金额。
func receiptMatches(request pay.RefundRequest, result pay.RefundResult) bool {
	return result.RefundNo == request.RefundNo &&
		result.MerchantOrderNo == request.MerchantOrderNo &&
		result.TransactionID == request.TransactionID &&
		result.TotalCents == request.TotalCents &&
		result.RefundCents == request.RefundCents &&
		result.RefundID != "" && len(result.RefundID) <= 64
}

// providerMatchesChannel 检查交易来源与渠道一致：SIM 交易只允许 simulation，真实交易禁止模拟确认。
func providerMatchesChannel(providerName, transactionID string) bool {
	simulated := strings.HasPrefix(transactionID, "SIM")
	return (providerName == "simulation") == simulated
}

// apply 核对渠道回执；PROCESSING 保留原状态，CLOSED/ABNORMAL 在钱包→退款锁内释放预占。
// SUCCESS 按钱包（适用时）→支付→退款加锁，保存已退款金额、回执、订单和 Outbox。
// 重放不重复出账，错误整体回滚，事务内不请求渠道。
func (e RefundExecutor) apply(ctx context.Context, r RefundRecord, request pay.RefundRequest, result pay.RefundResult) error {
	if !receiptMatches(request, result) {
		return ErrRefundConflict
	}
	switch result.Status {
	case "PROCESSING":
		return nil
	case "CLOSED", "ABNORMAL":
		return e.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := wallet.LockRefundWallet(tx, walletSettlement(r)); err != nil {
				return err
			}
			var current RefundRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&current).Error; err != nil {
				return err
			}
			if current.Status == money.RefundFailed {
				return nil
			}
			if current.Status != money.RefundProcessing {
				return ErrRefundConflict
			}
			if err := wallet.ReleaseFailedRefund(tx, walletSettlement(r)); err != nil {
				return err
			}
			return tx.Model(&RefundRecord{}).Where("id=?", r.ID).Updates(map[string]any{"status": money.RefundFailed, "wechat_refund_id": result.RefundID, "failure_reason": "provider refund " + result.Status}).Error
		})
	case "SUCCESS":
		if result.SuccessAt.IsZero() {
			return ErrRefundConflict
		}
	default:
		return ErrRefundConflict
	}
	return e.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := wallet.LockRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		var p pay.RefundPayment
		var paymentErr error
		p, paymentErr = pay.LockPaymentForRefundReceipt(tx, r.PaymentOrderID)
		if err := paymentErr; err != nil {
			return err
		}
		var current RefundRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", r.ID).Take(&current).Error; err != nil {
			return err
		}
		var receipt struct {
			RefundRecordID uint64 // 退款主键，一笔一份成功回执
			WechatRefundID string // 渠道退款单号
		}
		// 钱包锁前的查询可能建立旧快照，成功回执用当前读以识别已入账重放。
		err := tx.Table("refund_success_receipt").Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_record_id = ?", r.ID).Take(&receipt).Error
		if err == nil {
			if current.Status != "success" || receipt.WechatRefundID != result.RefundID {
				return ErrRefundConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Status != "processing" || current.RefundCents != result.RefundCents || p.TotalCents != result.TotalCents || p.WechatTransactionID.String != result.TransactionID || p.RefundedCents < 0 || result.RefundCents > p.PaidCents-p.RefundedCents {
			return ErrRefundConflict
		}
		if err := tx.Table("refund_success_receipt").Create(map[string]any{"refund_record_id": r.ID, "wechat_refund_id": result.RefundID}).Error; err != nil {
			return err
		}
		total := p.RefundedCents + result.RefundCents
		status := money.PayStatusPartialRefunded
		if total == p.PaidCents {
			status = money.PayStatusRefunded
		}
		if err := pay.SetConfirmedRefundInTx(tx, p.ID, total, status); err != nil {
			return err
		}
		if err := orderstore.SyncOrderPaymentStatus(tx, p.ID); err != nil {
			return err
		}
		if err := tx.Model(&RefundRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": money.RefundSuccess, "wechat_refund_id": result.RefundID, "completed_at": result.SuccessAt.UTC(), "failure_reason": nil}).Error; err != nil {
			return err
		}
		if err := wallet.SettleRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		if r.BizType == money.PaymentBizCharge && r.BizID != 0 {
			if status == money.PayStatusRefunded {
				if err := orderstore.CompleteChannelRefundInTx(tx, r.BizID, p.ID, true); err != nil {
					return err
				}
			} else {
				var pending int64
				if err := tx.Model(&RefundRecord{}).Where("payment_order_id=? AND status IN ? AND deleted_at IS NULL", p.ID, unsettledRefundStatuses()).Count(&pending).Error; err != nil {
					return err
				}
				if pending == 0 {
					if err := orderstore.CompleteChannelRefundInTx(tx, r.BizID, p.ID, false); err != nil {
						return err
					}
				}
			}
		}
		digest := sha256.Sum256([]byte("refund-success:" + r.RefundNo))
		eventID := "RS" + hex.EncodeToString(digest[:16])
		body, err := json.Marshal(map[string]any{"event_id": eventID, "refund_no": r.RefundNo, "payment_order_id": p.ID, "charge_order_id": r.BizID, "user_id": r.UserID, "refund_cents": result.RefundCents, "provider_refund_id": result.RefundID})
		if err != nil {
			return err
		}
		return ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: eventID, Stream: "refund_succeeded_stream", EnvelopeJSON: body})
	})
}
