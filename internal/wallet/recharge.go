package wallet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"time"
)

// RechargePayment 是 payment 提供的支付单事实，原预支付参数以 JSON 保持渠道形状。
type RechargePayment struct {
	ID          uint64     // 原支付主键，内部契约使用整数
	OrderNo     string     // 原商户单号，预支付重试不改变
	AmountCents int64      // 支付金额，单位分
	Status      string     // 当前支付状态，客户端支付成功不能修改此值
	ExpiresAt   *time.Time // UTC 支付截止，nil 表示没有有效期限
}

// RechargePayments 是钱包申请需要的支付能力；实现由 cmd 注入，钱包不引用 payment。
type RechargePayments interface {
	// LoadInTx 在 tx 内读取 paymentID 的原支付事实；无记录或读取失败返回错误。
	LoadInTx(tx *gorm.DB, paymentID uint64) (RechargePayment, error)
	// CreateInTx 在 tx 内为 userID 创建 amountCents（分）的充值支付；调用方负责提交。
	CreateInTx(tx *gorm.DB, userID uint64, amountCents int64) (RechargePayment, error)
	// Prepare 在申请已提交后为原支付单请求或复用预支付参数。
	// ctx 取消数据库和渠道调用；失败保留原单号，参数为空时必须返回错误。
	Prepare(ctx context.Context, pay RechargePayment, openID string) (json.RawMessage, error)
}

// RechargeService 管理充值申请事务和提交后的预支付准备。
type RechargeService struct {
	DB       *gorm.DB         // central 钱包请求数据，生命周期由进程管理
	Payments RechargePayments // payment 的原单复用和渠道准备能力
}

// RechargeInput 保留当前充值请求的 JSON 内容用于幂等存档。
type RechargeInput struct {
	RequestID   string `json:"request_id"`   // 客户端 UUID
	AmountCents int64  `json:"amount_cents"` // 申请充值金额，分，100–5000000
}

// RechargeResult 是可公开的申请回执，支付参数仅在原单仍可付款时出现。
type RechargeResult struct {
	RequestID       string          `json:"request_id"`               // 原申请身份
	PaymentOrderID  uint64          `json:"payment_order_id"`         // 原支付身份，保持既有公开整数契约
	MerchantOrderNo string          `json:"merchant_order_no"`        // 原商户单号
	AmountCents     int64           `json:"amount_cents"`             // 申请金额，分
	Status          string          `json:"status"`                   // 已持久支付状态
	CanPay          bool            `json:"can_pay"`                  // 未支付且未过期才为 true
	PaymentParams   json.RawMessage `json:"payment_params,omitempty"` // 已保存的渠道参数，不含服务密钥
}

// Create 为 userID 提交 in 的充值申请；ctx 取消事务和提交后预支付调用。
// 成功返回原支付回执；冲突、冻结或无身份时返回相应业务错误，结果不可使用。
// 锁序沿用 user→wallet→request，申请与支付同事务；预支付必须在提交后执行。
// 使用 READ COMMITTED，使读取 openid 后等待用户／钱包锁的重放仍能看见前一申请的已提交凭据。
// 渠道失败返回 ErrPrepayUnavailable，原申请仍持久存在，相同 request_id 可恢复原支付参数。
func (s RechargeService) Create(ctx context.Context, userID uint64, in RechargeInput) (RechargeResult, error) {
	if s.DB == nil || s.Payments == nil || userID == 0 || uuid.Validate(in.RequestID) != nil || in.AmountCents < 100 || in.AmountCents > 5000000 {
		return RechargeResult{}, ErrRechargeConflict
	}
	repo := UserWalletRepo{DB: s.DB}
	var openID string
	var pay RechargePayment
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		openID, err = repo.FindUserOpenID(tx, userID)
		if err != nil || openID == "" {
			return ErrIdentity
		}
		if _, err := repo.LockUser(tx, userID); err != nil {
			return err
		}
		account, err := repo.LockWalletAccount(tx, userID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := repo.OpenWalletAccount(tx, userID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if account.Status != "active" {
			return ErrFrozen
		}
		prior, err := repo.FindRechargeRequest(tx, in.RequestID)
		if err == nil {
			if prior.UserID != userID || prior.AmountCents != in.AmountCents {
				return ErrRechargeConflict
			}
			pay, err = s.Payments.LoadInTx(tx, prior.PaymentOrderID)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		pay, err = s.Payments.CreateInTx(tx, userID, in.AmountCents)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		return repo.InsertRechargeRequest(tx, WalletRechargeRequest{RequestID: in.RequestID, UserID: userID, AmountCents: in.AmountCents, PaymentOrderID: pay.ID, RequestJSON: string(payload)})
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RechargeResult{}, err
	}
	out := RechargeResult{RequestID: in.RequestID, PaymentOrderID: pay.ID, MerchantOrderNo: pay.OrderNo, AmountCents: pay.AmountCents, Status: pay.Status, CanPay: pay.Status == "initiated" && pay.ExpiresAt != nil && pay.ExpiresAt.After(time.Now())}
	if out.CanPay {
		out.PaymentParams, err = s.Payments.Prepare(ctx, pay, openID)
		if err != nil {
			return RechargeResult{}, errors.Join(ErrPrepayUnavailable, err)
		}
	}
	return out, nil
}
