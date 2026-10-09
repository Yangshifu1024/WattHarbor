package wallet

import (
	"chargepilot/internal/persist"
	"context"
	"time"
)

// AdminRechargeRow 保存后台充值历史的支付字段；PaidCents 是已确认到账金额。
type AdminRechargeRow struct {
	PaymentOrderID uint64     `json:"payment_order_id"` // 支付主键，保持既有整数契约
	OrderNo        string     `json:"order_no"`         // 商户支付单号
	UserID         uint64     `json:"user_id,string"`   // 付款人，公开为十进制字符串
	BizType        string     `json:"biz_type"`         // charge 充电支付，wallet_recharge 余额充值。
	PayMethod      string     `json:"pay_method"`       // wechat 微信支付，balance 余额支付。
	Status         string     `json:"status"`           // 支付单内部流程状态，保留关闭和失败等原因。
	PaymentStatus  string     `json:"payment_status"`   // 金额确认后的 pending/paid/refunded/partial_refunded。
	TotalCents     int64      `json:"total_cents"`      // 应付金额，分
	PaidCents      int64      `json:"paid_cents"`       // 已确认到账金额，分
	RefundedCents  int64      `json:"refunded_cents"`   // 累计成功退款金额，单位分。
	PaidAt         *time.Time `json:"paid_at"`          // 已确认到账时刻，未到账为空
	CreatedAt      time.Time  `json:"created_at"`       // 支付创建时刻
	ChargeOrderID  *uint64    `json:"charge_order_id"`  // 当前有效关联充电主键，充值或无关联为空
	ChargeOrderNo  *string    `json:"charge_order_no"`  // 当前有效关联充电单号，充值或无关联为空
}

// RechargeHistoryReader 仅查询用户的充值支付页，不提供资金写入。
type RechargeHistoryReader interface {
	// WalletRecharges 读取 userID 充值，page 为已校验分页；返回分页结果。
	WalletRecharges(ctx context.Context, userID uint64, page persist.PageQuery) (persist.Page[AdminRechargeRow], error)
}

// RechargeHistoryService 提供后台用户所属充值记录；用户存在性和权限由身份用例核对。
type RechargeHistoryService struct {
	Payments RechargeHistoryReader /* cmd 注入支付所属读取能力 */
}

// History 返回 userID 的 page 充值页，不把待支付单视为已到账。
func (s RechargeHistoryService) History(ctx context.Context, userID uint64, page persist.PageQuery) (persist.Page[AdminRechargeRow], error) {
	return s.Payments.WalletRecharges(ctx, userID, page)
}
