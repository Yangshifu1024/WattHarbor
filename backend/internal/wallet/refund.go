package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
	"time"
)

var (
	ErrFrozen              = errors.New("wallet is frozen")                        // 钱包当前不可发起消费或新退款
	ErrInsufficientBalance = errors.New("insufficient available wallet balance")   // 可用余额不足，单位分
	ErrIdentity            = errors.New("wallet owner identity unavailable")       // 无有效渠道用户身份
	ErrRechargeConflict    = errors.New("wallet recharge request conflicts")       // 同请求号的申请人或金额不同
	ErrPrepayNotConfigured = errors.New("wallet recharge provider not configured") // 申请已保存但未配置渠道
	ErrPrepayUnavailable   = errors.New("wallet recharge prepay unavailable")      // 保留原支付单供相同请求重试
)

// RefundInput 是用户退款申请的业务输入，不包含 HTTP 身份或数据库模型。
type RefundInput struct {
	RequestID   string `json:"request_id"`   // 客户端 UUID，重放沿用此号
	AmountCents int64  `json:"amount_cents"` // 申请原路退回金额，分，1–5000000
	Reason      string `json:"reason"`       // 退款依据，最多 255 字，比较前去除首尾空白
}

// RechargeSource 是可原路退回的已支付充值事实，数据库映射留在 payment。
type RechargeSource struct {
	ID                   uint64 // 原支付主键
	BizID                uint64 // 原充值业务身份
	PaidCents            int64  // 渠道已收金额，单位分
	TotalCents           int64  // 原申请金额，单位分
	RefundedCents        int64  // 已成功退回金额，单位分
	TransactionConfirmed bool   // 是否具有渠道交易号
}

// RechargeSourceReader 按原有钱包→支付锁顺序读取原渠道来源。
type RechargeSourceReader interface {
	// LockRechargeSources 在 tx 内锁 userID 的有效微信充值支付，按主键递增返回。
	// 无来源返回空集合、nil；数据库错误要求整个预占事务回滚。
	LockRechargeSources(tx *gorm.DB, userID uint64) ([]RechargeSource, error)
}

// RefundRecorder 由 settlement 提供退款创建及在途聚合，不暴露其 ORM 模型。
type RefundRecorder interface {
	// ReservedInTx 当前读 paymentID 的在途退款金额（分），调用方已锁原支付。
	// 返回 0、nil 表示无占用，错误要求回滚。
	ReservedInTx(tx *gorm.DB, paymentID uint64) (int64, error)
	// CreateInTx 在调用方 tx 内按 in 创建原单号退款；返回主键及错误。
	// 记录、钱包冻结和来源分摊必须由调用方一并提交，不在此调用渠道。
	CreateInTx(tx *gorm.DB, in money.RefundDraft) (uint64, error)
}

// RefundService 持有钱包退款申请及预占所需的窄能力，cmd 负责注入。
type RefundService struct {
	DB       *gorm.DB             // 所属 central 数据库；请求及预占事务由本用例管理
	Sources  RechargeSourceReader // 原支付来源及行锁，由 payment 实现
	Recorder RefundRecorder       // 原退款凭据及预占当前读，由 settlement 实现
}

// Request 为 userID 提交 in 的余额退款；ctx 取消整个请求事务。
// 返回首次回执或同内容重放回执；error 非 nil 时结果不可使用，业务冲突不能换号绕过。
// 锁序为钱包→申请→原支付，在途额度用当前读；申请、频次冻结、拆单和回执同事务。
// accepted 只表示持久预占，manual_review 表示需风控审核，均不表示渠道资金已退回。
func (s RefundService) Request(ctx context.Context, userID uint64, in RefundInput) (map[string]any, error) {
	if s.DB == nil || userID == 0 || uuid.Validate(in.RequestID) != nil || in.AmountCents <= 0 || in.AmountCents > 5000000 || len([]rune(in.Reason)) > 255 {
		return nil, ErrWalletRefundConflict
	}
	in.Reason = strings.TrimSpace(in.Reason)
	var response map[string]any
	repo := UserWalletRepo{DB: s.DB}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account, err := repo.LockWalletAccount(tx, userID)
		if err != nil {
			return err
		}
		prior, err := repo.FindRefundRequest(tx, in.RequestID)
		if err == nil {
			reason := ""
			if prior.Reason != nil {
				reason = *prior.Reason
			}
			if prior.UserID != userID || prior.AmountCents != in.AmountCents || reason != in.Reason {
				return ErrWalletRefundConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if account.Status != "active" {
			return ErrFrozen
		}
		if account.BalanceCents-account.FrozenCents < in.AmountCents {
			return ErrInsufficientBalance
		}
		var reason *string
		if in.Reason != "" {
			reason = &in.Reason
		}
		if err := repo.InsertRefundRequest(tx, in.RequestID, userID, in.AmountCents, reason); err != nil {
			return err
		}
		count, err := repo.CountRecentRefundRequests(tx, userID, time.Now().UTC().Add(-5*time.Minute))
		if err != nil {
			return err
		}
		status := "accepted"
		if count >= 3 {
			status = "manual_review"
			freezeID, err := repo.InsertRiskFreeze(tx, userID, "wallet_refund_frequency", "wallet", "wallet refund frequency")
			if err != nil {
				return err
			}
			if err := repo.LinkFreezeToRefund(tx, in.RequestID, freezeID); err != nil {
				return err
			}
			if err := repo.FreezeWallet(tx, account.ID); err != nil {
				return err
			}
		} else if err := s.ReserveInTx(tx, WalletRefundReservation{RequestID: in.RequestID, UserID: userID, AmountCents: in.AmountCents, Reason: in.Reason}); err != nil {
			return err
		}
		response = map[string]any{"request_id": in.RequestID, "status": status, "refund_cents": in.AmountCents}
		payload, err := json.Marshal(response)
		if err != nil {
			return err
		}
		return repo.SetRefundResponse(tx, in.RequestID, string(payload))
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}
