package settlement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	payment "github.com/ChargePilot2026/charge-pilot/internal/payment"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SignerStatus 是审核人的实时身份快照，由调用方（admin 授权域）注入查询。
// 双人复核的第二签必须核对前一审核人的实时角色与权限，不沿用首签时的会话结论。
type SignerStatus struct {
	Role      string // 当前有效角色；第二签要求 customer_finance
	Permitted bool   // 当前是否仍有退款复核权限
}

// RefundReviewRepo 承载人工退款的工作流写入：建单（幂等重放）、双人复核、执行重投。
// refund_review / refund_rejection / manual_refund_request / refund_record 的写入归属本包，
// RefundAdminService 负责授权与事务内审计，HTTP 仅适配请求和响应。
// 方法不自行开事务：调用方须传入已挂载审计与幂等语义的 *gorm.DB 事务。
type RefundReviewRepo struct {
	DB *gorm.DB // 结算数据库句柄；写方法使用显式传入的事务
	// LookupSigner 查询审核人的实时角色与权限；nil 时第二签一律冲突。
	LookupSigner func(ctx context.Context, signerID uint64) (SignerStatus, error)
}

// ManualRefundInput 是人工建单的最小事实集合；ActorID 用于幂等请求体快照。
type ManualRefundInput struct {
	ActorID     uint64 // 发起人工退款的管理员身份，参与重放内容核对
	OrderID     uint64 // 已终止的充电订单主键
	RequestID   string // 人工申请幂等号，同号必须提交相同内容
	AmountCents int64  // 本次申请退款额，分；由用例校验为正数
	Reason      string // 退款业务原因，参与重放内容核对
}

// manualRefundNo 根据 requestID 生成稳定退款单号；无 IO 或错误返回。
func manualRefundNo(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return "MR" + hex.EncodeToString(sum[:16])
}

// ManualCreate 在调用方事务中锁定支付单，校验订单可退及累计退款不超额，
// 再创建待双人复核的退款记录。同一 request_id、相同内容返回原退款单（created=false）；
// ctx 控制数据库请求，tx 须由用例管理提交与审计，in 已完成身份与输入校验。
// 新建返回 created、退款号及记录 ID；重放返回原号且 ID 为零。
// 同号不同内容或状态校验失败返回 ErrRefundConflict，查询与写入错误原样返回。
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
	Reason         string // 驳回原因，仅驳回分支使用
	ActorID        uint64 // 当前审核管理员身份，须由用例实时授权
}

// reviewRow 是 refund_review 的读取投影，每笔退款至多一条。
type reviewRow struct {
	RefundRecordID uint64  // 关联的退款记录主键 refund_record.id，一对一
	SnapshotJSON   string  // 第一签时冻结的退款要素 JSON，第二签据此比对，内容不一致即冲突
	FirstSigner    uint64  // 第一审核人 ID
	FirstComment   string  // 第一审核人的审核意见
	SecondSigner   *uint64 // 第二审核人 ID，nil 表示第二签还没做
	SecondComment  *string // 第二审核人的审核意见，nil 表示第二签还没做
}

// refundSnapshot 将退款的业务要素编码为规范 JSON，作为双人复核快照。
// r 提供支付、用户、业务身份及金额；返回规范 JSON，无 IO 或错误返回。
// 状态、重试次数和时间戳在审核期间可能变化，不参与一致性比较。
func refundSnapshot(r RefundRecord) string {
	b, _ := json.Marshal(map[string]any{"refund_no": r.RefundNo, "payment_order_id": r.PaymentOrderID, "user_id": r.UserID, "biz_type": r.BizType, "biz_id": r.BizID, "refund_cents": r.RefundCents})
	return string(b)
}

// Review 按决定执行通过或驳回，仅接受 pending、manual_review 退款，须在调用方事务内执行。
// 首签进入 awaiting_second；不同审核人第二签（实时身份经 LookupSigner 核对）后返回 approved，
// 退款改为 automatic 并立即安排执行。驳回写入 refund_rejection 并将退款置为 rejected，无需第二签。
// ctx 控制读取，tx 管理支付→退款锁及写入，refundNo 标识退款，in 提供已授权决定。
// 返回决定状态和退款记录 ID；状态、快照或首签身份不符返回 ErrRefundConflict，数据库错误原样返回。
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

// Retry 将 automatic、pending/processing 退款的 next_attempt_at 设为当前时间，
// ctx 控制数据库请求，tx 由用例管理，refundNo 标识退款；返回记录 ID 供审计使用。
// 不修改金额或状态；人工复核或终态返回 ErrRefundConflict，缺记录和存储错误原样返回。
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

// oneOf 判断 value 是否匹配 options 中任一空白分隔值；返回匹配结果，无 IO 或错误。
func oneOf(value string, options string) bool {
	for _, option := range strings.Fields(options) {
		if value == option {
			return true
		}
	}
	return false
}
