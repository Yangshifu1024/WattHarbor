package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
	"wattharbor/internal/domain/money"
	"wattharbor/internal/wallet"
)

// RechargePaymentRepo 实现钱包充值的支付能力，支付和参数由 payment 所有。
type RechargePaymentRepo struct {
	DB       *gorm.DB       // 提交后复用预支付参数的 central 连接
	Provider PrepayProvider // 渠道预支付能力；nil 时请求明确失败
}

// rechargePaymentView 将支付记录转换为钱包所需的编号、金额（分）、状态与可选 UTC 有效期。
func rechargePaymentView(r PaymentOrderRecord) wallet.RechargePayment {
	var expiry *time.Time
	if r.ExpiredAt.Valid {
		expiry = &r.ExpiredAt.Time
	}
	return wallet.RechargePayment{ID: r.ID, OrderNo: r.OrderNo, AmountCents: r.TotalCents, Status: r.Status, ExpiresAt: expiry}
}

// LoadInTx 在调用方事务中读取 id 的充值支付，不修改支付状态。
// 返回业务查询结果或数据库错误；调用方已核对充值申请所有人并负责事务生命周期。
func (RechargePaymentRepo) LoadInTx(tx *gorm.DB, id uint64) (wallet.RechargePayment, error) {
	var r PaymentOrderRecord
	err := tx.Where("id=?", id).Take(&r).Error
	return rechargePaymentView(r), err
}

// CreateInTx 在调用方事务中为 userID 创建 amountCents 分的充值支付及原单号。
// 返回支付主键、单号和状态，或分配、插入错误；有效期 30 分钟，不调用渠道。
func (RechargePaymentRepo) CreateInTx(tx *gorm.DB, userID uint64, amountCents int64) (wallet.RechargePayment, error) {
	no, err := NewOrderNumber(tx)
	if err != nil {
		return wallet.RechargePayment{}, err
	}
	now := time.Now().UTC()
	r := PaymentOrderRecord{OrderNo: no, BizType: money.PaymentBizWalletRecharge, UserID: userID, PayMethod: money.PayMethodWechat, TotalCents: amountCents, Status: money.PayStatusInitiated, ExpiredAt: sql.NullTime{Time: now.Add(30 * time.Minute), Valid: true}, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
	if err := tx.Create(&r).Error; err != nil {
		return wallet.RechargePayment{}, err
	}
	return rechargePaymentView(r), nil
}

// Prepare 为已提交的 pay 复用或准备渠道参数；openID 是已核对申请人的微信身份。
// ctx 控制数据库和渠道调用；返回参数 JSON 或读取、渠道、解码错误。
// 并发保存以 payment_order_id 唯一键仲裁，重读首次参数；未知结果沿用原单号重试。
func (s RechargePaymentRepo) Prepare(ctx context.Context, pay wallet.RechargePayment, openID string) (json.RawMessage, error) {
	if s.DB == nil || pay.ExpiresAt == nil {
		return nil, errors.New("recharge payment not configured")
	}
	var stored ChargePrepayRecord
	var params PrepayParams
	err := s.DB.WithContext(ctx).Where("payment_order_id=?", pay.ID).Take(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if s.Provider == nil {
			return nil, wallet.ErrPrepayNotConfigured
		}
		params, err = s.Provider.Prepay(ctx, PrepayRequest{MerchantOrderNo: pay.OrderNo, OpenID: openID, AmountCents: pay.AmountCents, ExpiresAt: *pay.ExpiresAt})
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		stored = ChargePrepayRecord{PaymentOrderID: pay.ID, ParamsJSON: encoded, PrepayID: sql.NullString{String: params.PrepayID, Valid: params.PrepayID != ""}}
		if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&stored).Error; err != nil {
			return nil, err
		}
		err = s.DB.WithContext(ctx).Where("payment_order_id=?", pay.ID).Take(&stored).Error
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(stored.ParamsJSON, &params); err != nil {
		return nil, err
	}
	return json.Marshal(params)
}
