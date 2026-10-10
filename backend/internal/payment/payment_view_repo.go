package payment

import (
	"chargepilot/internal/domain/money"
	orderdomain "chargepilot/internal/domain/order"
	orderstore "chargepilot/internal/order"
	"chargepilot/internal/persist"
	"context"

	"time"

	"gorm.io/gorm"
)

// 本文件查询支付单列表和摘要，金额取已保存的支付及退款记录。

// PaymentOrderView 包含实际支付、余额扣费与充值记录；充电生命周期由关联充电单展示。
type PaymentOrderView struct {
	PaymentOrderID uint64                         `json:"payment_order_id"` // 支付主键，保持既有整数契约
	OrderNo        string                         `json:"order_no"`         // 商户支付单号
	UserID         uint64                         `json:"user_id,string"`   // 付款人，公开为十进制字符串
	BizType        money.PaymentBizType           `json:"biz_type"`         // charge 充电支付，wallet_recharge 余额充值。
	PayMethod      string                         `json:"pay_method"`       // wechat 微信支付，balance 余额支付。
	Status         money.PaymentStatus            `json:"status"`           // 支付单内部流程状态，保留关闭和失败等原因。
	PaymentStatus  orderdomain.OrderPaymentStatus `json:"payment_status"`   // 金额确认后的 pending/paid/refunded/partial_refunded。
	TotalCents     int64                          `json:"total_cents"`      // 应付金额，分
	PaidCents      int64                          `json:"paid_cents"`       // 已确认到账金额，分
	RefundedCents  int64                          `json:"refunded_cents"`   // 累计成功退款金额，单位分。
	PaidAt         *time.Time                     `json:"paid_at"`          // 已确认到账时刻，未到账为空
	CreatedAt      time.Time                      `json:"created_at"`       // 支付创建时刻
	ChargeOrderID  *uint64                        `json:"charge_order_id"`  // 当前有效关联充电主键，充值或无关联为空
	ChargeOrderNo  *string                        `json:"charge_order_no"`  // 当前有效关联充电单号，充值或无关联为空
}

// PaymentOrderQuery 保存支付单列表的分页和筛选条件。
type PaymentOrderQuery struct {
	persist.PageQuery            // 页号、页大小及通用筛选；本查询使用页号和页大小
	UserID            uint64     // 内部用户范围；0 表示不筛选，通用支付单接口不读取此字段。
	OrderNo           string     // 支付单号，空值不限
	BizType           string     // charge 或 wallet_recharge，空值不限
	PayMethod         string     // wechat 或 balance，空值不限
	PaymentStatus     string     // 独立金额确认状态，空值不限
	From              *time.Time // 创建时间起点，含边界，可空
	To                *time.Time // 创建时间终点，含边界，可空
}

// PaymentOrders 直接读取支付单，充值不会被误列为充电订单。
// ctx 取消 db 查询，q 提供分页、用户及支付筛选；返回稳定倒序分页结果、总数，无资金写入。
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
	OrderNo       string              `gorm:"column:order_no" json:"order_no"`             // 商户支付单号
	TotalCents    int64               `gorm:"column:total_cents" json:"total_cents"`       // 应付金额，分
	PaidCents     int64               `gorm:"column:paid_cents" json:"paid_cents"`         // 已确认到账金额，分
	RefundedCents int64               `gorm:"column:refunded_cents" json:"refunded_cents"` // 累计成功退回，分
	Status        money.PaymentStatus `gorm:"column:status" json:"status"`                 // 支付生命周期状态
}

// FindOrderPaymentBrief 查询未删除支付单的金额与状态，缺失时返回 gorm.ErrRecordNotFound。
// 调用方可据此显示没有支付单，不把缺失当作数据库故障。
func FindOrderPaymentBrief(ctx context.Context, db *gorm.DB, paymentOrderID uint64) (OrderPaymentBrief, error) {
	var row OrderPaymentBrief
	err := db.WithContext(ctx).Table("payment_order").
		Select("order_no, total_cents, paid_cents, refunded_cents, status").
		Where("id = ? AND deleted_at IS NULL", paymentOrderID).Take(&row).Error
	return row, err
}
