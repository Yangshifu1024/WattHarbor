package settlement

import (
	orderstore "chargepilot/internal/order"
	payment "chargepilot/internal/payment"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SignerStatus 是审核人的实时身份快照，由调用方（admin 授权域）注入查询。
// 双人复核的第二签必须核对前一审核人的实时角色与权限，不沿用首签时的会话结论。
type SignerStatus struct {
	Role      string // 当前有效角色；第二签要求 customer_finance
	Permitted bool   // 当前是否仍有退款复核权限
}

// RefundReviewRepo 在调用方事务内处理人工退款建单、双审和重投。
// RefundAdminService 负责实时权限与同事务审计，HTTP 只转换请求和响应。
type RefundReviewRepo struct {
	DB *gorm.DB // 结算数据库句柄；写方法使用显式传入的事务
	// LookupSigner 查询审核人的实时角色与权限；nil 时第二签一律冲突。
	LookupSigner func(ctx context.Context, signerID uint64) (SignerStatus, error)
}

// ManualRefundInput 保存退款业务内容，ActorID 参与请求快照比对。
type ManualRefundInput struct {
	ActorID     uint64 // 发起人工退款的管理员 ID，参与重放内容核对
	OrderID     uint64 // 已终止的充电订单主键
	RequestID   string // 人工申请幂等号，同号必须提交相同内容
	AmountCents int64  // 本次申请退款额，分；由用例校验为正数
	Reason      string // 退款业务因，参与重放内容核对
}

// manualRefundNo 根据请求 ID 生成稳定退款号。
func manualRefundNo(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return "MR" + hex.EncodeToString(sum[:16])
}

// ManualCreate 在支付锁内核对订单和累计可退金额，再登记待双审退款。
// 同一 request_id 且内容一致时返回首次退款号、created=false 和 ID=0；首次返回新主键。
// 同号异内容或状态不符返回 ErrRefundConflict；用例负责同事务审计和提交。
func (s RefundReviewRepo) ManualCreate(ctx context.Context, tx *gorm.DB, in ManualRefundInput) (created bool, refundNo string, recordID uint64, err error) {
	refundNo = manualRefundNo(in.RequestID)
	payload, _ := json.Marshal(map[string]any{"actor_id": in.ActorID, "order_id": in.OrderID, "amount_cents": in.AmountCents, "reason": in.Reason})
	tx = tx.WithContext(ctx)
	err = func() error {
		var order orderstore.ChargeOrderRecord
		if err := tx.Where("id=? AND deleted_at IS NULL", in.OrderID).Take(&order).Error; err != nil {
			return err
		}
		// payment 提供所属支付的当前读取；保持支付→退款锁顺序，不反向引用 settlement。
		var pay payment.RefundPayment
		var paymentErr error
		pay, paymentErr = payment.LockPaymentForRefund(tx, uint64(order.PaymentOrderID.Int64))
		if err := paymentErr; err != nil {
			return err
		}
		var receipt struct {
			PayloadJSON string // 同一 request_id 首次提交时的请求体快照，重放时用于逐字段比对内容是否一致
			RefundNo    string // 首次生成的退款单号，重放时直接返回它，保证同号始终对应同一笔退款
		}
		// 支付锁不会刷新前面的订单快照，重放凭据须当前读，否则会把相同申请误判为新退款。
		e := tx.Table("manual_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", in.RequestID).Take(&receipt).Error
		if e == nil {
			var x, y any
			_ = json.Unmarshal([]byte(receipt.PayloadJSON), &x)
			_ = json.Unmarshal(payload, &y)
			xb, _ := json.Marshal(x)
			yb, _ := json.Marshal(y)
			if string(xb) != string(yb) {
				return ErrRefundConflict
			}
			refundNo = receipt.RefundNo
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", in.OrderID).Take(&order).Error; err != nil {
			return err
		}
		if !oneOf(order.Status, "completed cancelled failed refunded") || order.Status == "refunded" || pay.UserID != order.UserID || pay.PayMethod != "wechat" || !oneOf(pay.Status, "paid partial_refunded") || in.AmountCents > pay.PaidCents-pay.RefundedCents {
			return ErrRefundConflict
		}
		// 初次读取订单可能已建立 RR 快照。支付行锁串行化同支付的退款写入，
		// 但不会刷新既有快照；待退聚合使用当前读，以纳入锁等待期间提交的预占。
		var pending int64
		if err := tx.Table("refund_record").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(SUM(refund_cents),0)").
			Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", pay.ID).
			Scan(&pending).Error; err != nil {
			return err
		}
		if pending > pay.PaidCents-pay.RefundedCents-in.AmountCents {
			return ErrRefundConflict
		}
		record := RefundRecord{RefundNo: refundNo, PaymentOrderID: pay.ID, UserID: pay.UserID, BizType: "charge", BizID: in.OrderID, RefundCents: in.AmountCents, Reason: sql.NullString{String: in.Reason, Valid: true}, Status: "pending", ExecutionPolicy: "manual_review", NextAttemptAt: time.Now().UTC(), CreatedMonth: dbutil.MonthStart()}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := tx.Table("manual_refund_request").Create(map[string]any{"request_id": in.RequestID, "payload_json": string(payload), "refund_no": refundNo}).Error; err != nil {
			return err
		}
		created = true
		recordID = record.ID
		return nil
	}()
	return created, refundNo, recordID, err
}

// ReviewDecision 是一笔复核决定；Approve=true 走双人通过，false 为单人驳回。
type ReviewDecision struct {
	Approve        bool   // true 为双人通过流程，false 为单人驳回
	ApproveComment string // 通过意见，分别保存为首签或第二签意见
	Reason         string // 驳回因，仅驳回分支使用
	ActorID        uint64 // 当前审核管理员 ID，须由用例实时授权
}

// reviewRow 是退款复核记录，每笔退款至多一条。
type reviewRow struct {
	RefundRecordID uint64  // 关联的退款记录主键 refund_record.id，一对一
	SnapshotJSON   string  // 第一签时冻结的退款要素 JSON，第二签据此比对，内容不一致即冲突
	FirstSigner    uint64  // 第一审核人 ID
	FirstComment   string  // 第一审核人的审核意见
	SecondSigner   *uint64 // 第二审核人 ID，nil 表示第二签还没做
	SecondComment  *string // 第二审核人的审核意见，nil 表示第二签还没做
}

// refundSnapshot 将退款支付、用户、业务 ID 和金额编码为双审快照。
// 审核期间可变化的状态、次数和时间不参与一致性比较。
func refundSnapshot(r RefundRecord) string {
	b, _ := json.Marshal(map[string]any{"refund_no": r.RefundNo, "payment_order_id": r.PaymentOrderID, "user_id": r.UserID, "biz_type": r.BizType, "biz_id": r.BizID, "refund_cents": r.RefundCents})
	return string(b)
}

// Review 在调用方事务中按支付→退款加锁，仅审核 pending/manual_review 退款。
// 首签进入 awaiting_second；不同且仍有权限的人二签后改为 automatic 并安排执行。
// 驳回只需一人，写入拒绝记录；状态、快照或首签身份不符返回 ErrRefundConflict。
func (s RefundReviewRepo) Review(ctx context.Context, tx *gorm.DB, refundNo string, in ReviewDecision) (string, uint64, error) {
	status := "rejected"
	recordID := uint64(0)
	var r RefundRecord
	if err := tx.WithContext(ctx).Where("refund_no=? AND deleted_at IS NULL", refundNo).Take(&r).Error; err != nil {
		return status, recordID, err
	}
	// payment 提供所属支付的当前读取；保持支付→退款锁顺序，不反向引用 settlement。
	var pay payment.RefundPayment
	var paymentErr error
	pay, paymentErr = payment.LockPaymentForRefund(tx, r.PaymentOrderID)
	if err := paymentErr; err != nil {
		return status, recordID, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&r).Error; err != nil {
		return status, recordID, err
	}
	recordID = r.ID
	if r.Status != "pending" || r.ExecutionPolicy != "manual_review" {
		return status, recordID, ErrRefundConflict
	}
	var v reviewRow
	// 首次退款读取可能建立旧 RR 快照；支付及退款锁内读取实际首签，不能重复创建复核行。
	e := tx.Table("refund_review").Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_record_id=?", r.ID).Take(&v).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return status, recordID, e
	}
	if !in.Approve {
		if err := tx.Table("refund_rejection").Create(map[string]any{"refund_record_id": r.ID, "actor_id": in.ActorID, "reason": in.Reason}).Error; err != nil {
			return status, recordID, err
		}
		return status, recordID, tx.Table("refund_record").Where("id=?", r.ID).Updates(map[string]any{"status": "rejected", "failure_reason": in.Reason}).Error
	}
	if pay.PayMethod != "wechat" || pay.UserID != r.UserID || pay.BizID != r.BizID || pay.BizType != r.BizType || r.RefundCents <= 0 || r.RefundCents > pay.PaidCents-pay.RefundedCents {
		return status, recordID, ErrRefundConflict
	}
	snapshot := refundSnapshot(r)
	if errors.Is(e, gorm.ErrRecordNotFound) {
		status = "awaiting_second"
		return status, recordID, tx.Table("refund_review").Create(map[string]any{"refund_record_id": r.ID, "snapshot_json": snapshot, "first_signer": in.ActorID, "first_comment": in.ApproveComment}).Error
	}
	var stored any
	_ = json.Unmarshal([]byte(v.SnapshotJSON), &stored)
	b, _ := json.Marshal(stored)
	if v.FirstSigner == in.ActorID || v.SecondSigner != nil || string(b) != snapshot {
		return status, recordID, ErrRefundConflict
	}
	if s.LookupSigner == nil {
		return status, recordID, ErrRefundConflict
	}
	first, err := s.LookupSigner(ctx, v.FirstSigner)
	if err != nil || first.Role != "customer_finance" || !first.Permitted {
		return status, recordID, ErrRefundConflict
	}
	status = "approved"
	if err := tx.Table("refund_review").Where("refund_record_id=?", r.ID).Updates(map[string]any{"second_signer": in.ActorID, "second_comment": in.ApproveComment, "approved_at": time.Now().UTC()}).Error; err != nil {
		return status, recordID, err
	}
	return status, recordID, tx.Table("refund_record").Where("id=?", r.ID).Updates(map[string]any{"execution_policy": "automatic", "next_attempt_at": time.Now().UTC()}).Error
}

// Retry 将 automatic 且 pending/processing 的退款立即排队，返回记录 ID 供审计。
// 金额和状态不变；人工待审或终态返回 ErrRefundConflict。
func (s RefundReviewRepo) Retry(ctx context.Context, tx *gorm.DB, refundNo string) (uint64, error) {
	var r RefundRecord
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_no=? AND deleted_at IS NULL", refundNo).Take(&r).Error; err != nil {
		return 0, err
	}
	if r.ExecutionPolicy != "automatic" || (r.Status != "pending" && r.Status != "processing") {
		return 0, ErrRefundConflict
	}
	return r.ID, tx.Table("refund_record").Where("id=?", r.ID).Update("next_attempt_at", time.Now().UTC()).Error
}

// oneOf 检查值是否属于空白分隔的允许集合。
func oneOf(value string, options string) bool {
	for _, option := range strings.Fields(options) {
		if value == option {
			return true
		}
	}
	return false
}
