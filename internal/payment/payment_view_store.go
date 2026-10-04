package payment

import (
	"context"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"

	"time"

	"gorm.io/gorm"
)

// 支付单的列表读模型与分页查询。支付状态与金额语义由订单/资金域定义，
// 这里只负责取数与投影。

// PaymentOrderView 包含实际支付、余额扣费与充值记录；充电生命周期由关联充电单展示。
type PaymentOrderView struct {
	PaymentOrderID uint64     `json:"payment_order_id"`
	OrderNo        string     `json:"order_no"`
	UserID         uint64     `json:"user_id,string"`
	BizType        string     `json:"biz_type"`       // charge 充电支付，wallet_recharge 余额充值。
	PayMethod      string     `json:"pay_method"`     // wechat 微信支付，balance 余额支付。
	Status         string     `json:"status"`         // 支付单内部流程状态，保留关闭和失败等原因。
	PaymentStatus  string     `json:"payment_status"` // 金额确认后的 pending/paid/refunded/partial_refunded。
	TotalCents     int64      `json:"total_cents"`
	PaidCents      int64      `json:"paid_cents"`
	RefundedCents  int64      `json:"refunded_cents"` // 累计成功退款金额，单位分。
	PaidAt         *time.Time `json:"paid_at"`
	CreatedAt      time.Time  `json:"created_at"`
	ChargeOrderID  *uint64    `json:"charge_order_id"`
	ChargeOrderNo  *string    `json:"charge_order_no"`
}

type PaymentOrderQuery struct {
	persist.PageQuery
	UserID                                     uint64 // 内部用户范围；0 表示不筛选，通用支付单接口不读取此字段。
	OrderNo, BizType, PayMethod, PaymentStatus string
	From, To                                   *time.Time
}

// PaymentOrders 直接读取支付单，充值不会被误列为充电订单。
func PaymentOrders(ctx context.Context, db *gorm.DB, q PaymentOrderQuery) (persist.Page[PaymentOrderView], error) {
	out := persist.Page[PaymentOrderView]{Items: []PaymentOrderView{}, Page: q.Page, PageSize: q.PageSize}
	query := db.WithContext(ctx).Table("payment_order AS p").Where("p.deleted_at IS NULL")
	if q.UserID != 0 {
		query = query.Where("p.user_id=?", q.UserID)
	}
	if q.OrderNo != "" {
		query = query.Where("p.order_no=?", q.OrderNo)
	}
	if q.BizType != "" {
		query = query.Where("p.biz_type=?", q.BizType)
	}
	if q.PayMethod != "" {
		query = query.Where("p.pay_method=?", q.PayMethod)
	}
	if q.PaymentStatus != "" {
		query = query.Where("("+orderstore.PaymentStatusSQL+")=?", q.PaymentStatus)
	}
	if q.From != nil {
		query = query.Where("p.created_at >= ?", q.From.UTC())
	}
	if q.To != nil {
		query = query.Where("p.created_at <= ?", q.To.UTC())
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	// 标量子查询限制为一条，历史异常关联也不会放大分页总数。充值单始终不关联充电单。
	linked := " FROM charge_order c WHERE p.biz_type='charge' AND c.payment_order_id=p.id AND c.user_id=p.user_id AND c.deleted_at IS NULL ORDER BY c.created_at DESC,c.id DESC LIMIT 1)"
	columns := "p.id AS payment_order_id,p.order_no,p.user_id,p.biz_type,p.pay_method,p.status," + orderstore.PaymentStatusSQL + " AS payment_status," +
		"p.total_cents,p.paid_cents,p.refunded_cents,p.paid_at,p.created_at," +
		"(SELECT c.id" + linked + " AS charge_order_id,(SELECT c.order_no" + linked + " AS charge_order_no"
	err := query.Select(columns).Order("p.created_at DESC,p.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error
	return out, err
}

// OrderPaymentBrief 是订单详情里的支付单摘要。
type OrderPaymentBrief struct {
	OrderNo       string `gorm:"column:order_no" json:"order_no"`
	TotalCents    int64  `gorm:"column:total_cents" json:"total_cents"`
	PaidCents     int64  `gorm:"column:paid_cents" json:"paid_cents"`
	RefundedCents int64  `gorm:"column:refunded_cents" json:"refunded_cents"`
	Status        string `gorm:"column:status" json:"status"`
}

// FindOrderPaymentBrief 取支付单摘要。取不到返回 gorm.ErrRecordNotFound，
// 调用方按"没有支付单"处理而不是报错。
func FindOrderPaymentBrief(ctx context.Context, db *gorm.DB, paymentOrderID uint64) (OrderPaymentBrief, error) {
	var row OrderPaymentBrief
	err := db.WithContext(ctx).Table("payment_order").
		Select("order_no, total_cents, paid_cents, refunded_cents, status").
		Where("id = ? AND deleted_at IS NULL", paymentOrderID).Take(&row).Error
	return row, err
}
