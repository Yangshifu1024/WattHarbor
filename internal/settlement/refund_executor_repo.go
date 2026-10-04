package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	pay "github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrRefundConflict 表示原退款身份、金额、渠道、状态或额度与持久凭据冲突。
var ErrRefundConflict = errors.New("refund identity or amount conflict")

// ErrRefundReferenceMissing 表示退款依赖的某张表已经查不到对应行：
// 支付单、钱包退款分摊行或钱包账户任一缺失；这些前置身份不能通过重新请求渠道创建。
//
// 已缺失的前置身份无法通过重新向渠道请求恢复，执行器将原退款置为失败以停止重捞。
var ErrRefundReferenceMissing = errors.New("refund references a record that no longer exists")

// walletSettlement 把退款行映射为钱包结算的最小事实集合。
// r 为原退款记录，返回其原主键、用户、单号、类型及分值，无 IO 或错误。
func walletSettlement(r RefundRecord) wallet.Settlement {
	return wallet.Settlement{ID: r.ID, UserID: r.UserID, RefundNo: r.RefundNo, BizType: r.BizType, RefundCents: r.RefundCents}
}

// RefundExecutor 只派发明确属于自动充电或钱包充值退款的请求。
// 结果未知的退款保持 processing 状态，每次幂等重试之前都先查询一次。
type RefundExecutor struct {
	DB           *gorm.DB           // central 退款连接，由进程管理
	Provider     pay.RefundProvider // 原渠道查询及创建退款能力，未知结果先查原单
	ProviderName string             // 当前渠道标签，必须与原预支付或交易身份一致
}

// Batch 在 ctx 下读取最多 20 笔已到期 automatic 退款并逐项执行，返回无错误处理笔数和首个失败。
// 未配置 Provider 返回 0、nil；取消后停止后续处理，部分成功不回滚，原失败记录按原身份恢复。
func (e RefundExecutor) Batch(ctx context.Context) (int, error) {
	if e.Provider == nil {
		return 0, nil
	}
	var rows []RefundRecord
	if err := e.DB.WithContext(ctx).Where("execution_policy='automatic' AND biz_type IN ('charge','wallet_recharge') AND status IN ('pending','processing') AND next_attempt_at <= UTC_TIMESTAMP(3) AND deleted_at IS NULL").Order("next_attempt_at,id").Limit(20).Find(&rows).Error; err != nil {
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

// Execute 在 ctx 下领取 id 原退款的持久重试窗口，提交后以十秒超时查询原渠道，缺单才创建。
// 未到期或已成功返回 nil；前置引用缺失置为失败，身份／额度冲突或渠道未知返回错误。
// 未知结果保留 processing 与原单号，后续先查，不释放钱包 frozen；成功回执在原资金事务内应用。
func (e RefundExecutor) Execute(ctx context.Context, id uint64) error {
	if e.Provider == nil || e.ProviderName == "" {
		return errors.New("refund provider unavailable")
	}
	r, request, claimed, err := e.claim(ctx, id)
	if err != nil {
		// claim 里的每一张引用表都先于退款单创建。查不到行就是永久性的：
		// 落成终态让 Batch 不再选中它，而不是每 10 秒重捞一次。
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
		_ = e.DB.WithContext(ctx).Model(&RefundRecord{}).Where("id = ? AND status='processing'", id).Update("failure_reason", "provider result unknown; query scheduled").Error
		return fmt.Errorf("refund %s: provider result unknown", r.RefundNo)
	}
	return e.apply(ctx, r, request, result)
}

// failPermanent 把不可能成功的退款落成终态。next_attempt_at 不再前推，
// 因为没有下一次；status 离开 pending/processing 后 Batch 不会再选中它。
// ctx 取消更新，id 为原退款主键，reason 为已脱敏原因；返回数据库错误，不请求渠道。
func (e RefundExecutor) failPermanent(ctx context.Context, id uint64, reason string) error {
	return e.DB.WithContext(ctx).Model(&RefundRecord{}).
		Where("id = ? AND status IN ('pending','processing')", id).
		Updates(map[string]any{"status": "failed", "failure_reason": reason}).Error
}

// claim 在 ctx 下按 id 读取原退款，按钱包（适用时）→支付→退款锁序核对原身份及当前额度。
// 返回原记录、渠道原请求、已领取标志及错误；false、nil 表示成功或尚未到期，不得发起新请求。
// processing、尝试次数及有界退避窗口先提交；错误整体回滚，原单号不变，不在持锁期间请求渠道。
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
		// payment 提供所属支付的行锁投影；settlement 单向引用 payment，后者不引用 settlement。
		var p pay.RefundPayment
		var paymentErr error
		p, paymentErr = pay.LockPaymentForRefund(tx, r.PaymentOrderID)
		if err := paymentErr; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&r).Error; err != nil {
			return err
		}
		if r.Status == "success" {
			return nil
		}
		if r.ExecutionPolicy != "automatic" || (r.BizType != "charge" && r.BizType != "wallet_recharge") || (r.Status != "pending" && r.Status != "processing") {
			return ErrRefundConflict
		}
		if r.NextAttemptAt.After(time.Now().UTC()) {
			return nil
		}
		if p.UserID != r.UserID || p.BizID != r.BizID || p.BizType != r.BizType || p.PayMethod != "wechat" || !p.WechatTransactionID.Valid || p.WechatTransactionID.String == "" || p.PaidCents != p.TotalCents || p.RefundedCents < 0 || r.RefundCents <= 0 || r.RefundCents > p.PaidCents-p.RefundedCents || (p.Status != "paid" && p.Status != "partial_refunded") {
			return ErrRefundConflict
		}
		if r.BizType == "charge" {
			var prepay struct {
				ParamsJSON []byte // 原预支付渠道参数，核对原渠道身份
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
		// 同上：payment_order 的行锁串行化不了并发新建的退款单，
		// 这里必须用加锁读（当前读）才看得到别人已经落库的占用额。
		if err := tx.Model(&RefundRecord{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(refund_cents),0)").
			Where("payment_order_id = ? AND id <> ? AND status IN ('pending','processing') AND deleted_at IS NULL", p.ID, r.ID).
			Scan(&reserved).Error; err != nil {
			return err
		}
		if reserved > p.PaidCents-p.RefundedCents-r.RefundCents {
			return ErrRefundConflict
		}
		request = pay.RefundRequest{RefundNo: r.RefundNo, MerchantOrderNo: p.OrderNo, TransactionID: p.WechatTransactionID.String, TotalCents: p.TotalCents, RefundCents: r.RefundCents}
		// 这把持久化的租约同时提供了崩溃之后的有界指数退避重试。
		delay := time.Duration(30*(1<<min(r.RetryCount, uint32(7)))) * time.Second
		update := tx.Model(&RefundRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": "processing", "retry_count": gorm.Expr("retry_count+1"), "next_attempt_at": time.Now().UTC().Add(delay)})
		if update.Error != nil {
			return update.Error
		}
		claimed = true
		return nil
	})
	return r, request, claimed, err
}

// receiptMatches 核对渠道回执与原请求的交易、退款身份及金额是否严格对应。
// request 为持久原请求，result 为渠道回执；返回严格一致标志，无 IO 或错误。
func receiptMatches(request pay.RefundRequest, result pay.RefundResult) bool {
	return result.RefundNo == request.RefundNo &&
		result.MerchantOrderNo == request.MerchantOrderNo &&
		result.TransactionID == request.TransactionID &&
		result.TotalCents == request.TotalCents &&
		result.RefundCents == request.RefundCents &&
		result.RefundID != "" && len(result.RefundID) <= 64
}

// providerMatchesChannel 保证交易与结算它的渠道同属真实或模拟一侧。
//
// SIM 前缀交易只允许 simulation 渠道；真实交易不能通过模拟渠道生成资金确认。
// providerName 为装配渠道，transactionID 为原支付交易号；返回同侧标志，无 IO 或错误。
func providerMatchesChannel(providerName, transactionID string) bool {
	simulated := strings.HasPrefix(transactionID, "SIM")
	return (providerName == "simulation") == simulated
}

// apply 在 ctx 下核对 r 原退款、request 原请求和 result 渠道回执后推进资金。
// PROCESSING 保持原记录等待查询；终态失败只释放钱包预占，SUCCESS 原子写资金、回执、订单与 Outbox。
// 适用时先锁钱包再锁支付和退款，重复同回执不重复出账；错误整体回滚，不在事务内请求渠道。
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
			if current.Status == "failed" {
				return nil
			}
			if current.Status != "processing" {
				return ErrRefundConflict
			}
			if err := wallet.ReleaseFailedRefund(tx, walletSettlement(r)); err != nil {
				return err
			}
			return tx.Model(&RefundRecord{}).Where("id=?", r.ID).Updates(map[string]any{"status": "failed", "wechat_refund_id": result.RefundID, "failure_reason": "provider refund " + result.Status}).Error
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
			RefundRecordID uint64 // 原退款主键，一笔一份成功回执
			WechatRefundID string // 原渠道退款成功身份
		}
		// 钱包分摊的普通读取早于钱包锁；原成功回执使用当前读，不在旧快照下拒绝已成功重放。
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
		status := "partial_refunded"
		if total == p.PaidCents {
			status = "refunded"
		}
		if err := pay.SetConfirmedRefundInTx(tx, p.ID, total, status); err != nil {
			return err
		}
		if err := orderstore.SyncOrderPaymentStatus(tx, p.ID); err != nil {
			return err
		}
		if err := tx.Model(&RefundRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": "success", "wechat_refund_id": result.RefundID, "completed_at": result.SuccessAt.UTC(), "failure_reason": nil}).Error; err != nil {
			return err
		}
		if err := wallet.SettleRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		if r.BizType == "charge" && r.BizID != 0 {
			if status == "refunded" {
				if err := orderstore.CompleteChannelRefundInTx(tx, r.BizID, p.ID, true); err != nil {
					return err
				}
			} else {
				var pending int64
				if err := tx.Model(&RefundRecord{}).Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", p.ID).Count(&pending).Error; err != nil {
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
