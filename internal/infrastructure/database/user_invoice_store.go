package database

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// 用户侧的发票申请。运营侧的复核在 settlement_invoice_store.go，这里是
// 用户提交与查看。
//
// 金额永远取自订单，绝不取自请求体：客户端报多少就开发多少，等于让用户自己
// 决定发票金额。订单归属与结算状态也在查询条件里一并核对。

// InvoiceApplyOrder 是申请发票时要核对的订单信息。
type InvoiceApplyOrder struct {
	ID     uint64
	UserID uint64
	Status string
}

// FindInvoiceableOrder 按订单号与用户取订单。用户只能给自己的订单开票。
func FindInvoiceableOrder(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (InvoiceApplyOrder, error) {
	var row struct {
		ID     uint64 `gorm:"column:id"`
		UserID uint64 `gorm:"column:user_id"`
		Status string `gorm:"column:status"`
	}
	err := db.WithContext(ctx).Table("charge_order").Select("id, user_id, status").
		Where("order_no = ? AND user_id = ? AND deleted_at IS NULL", orderNo, userID).Take(&row).Error
	return InvoiceApplyOrder{ID: row.ID, UserID: row.UserID, Status: row.Status}, err
}

// ErrInvoiceDuplicate 表示该笔业务已开过票。
var ErrInvoiceDuplicate = errors.New("该订单已申请过发票")

// InvoiceApplyInput 是一次发票申请。TotalCents 由调用方从订单读出后传入，
// 不是客户端提交的值。
type InvoiceApplyInput struct {
	InvoiceNo   string
	UserID      uint64
	OrderID     uint64
	TotalCents  int64
	InvoiceType string
	Title       string
	TaxNo       string
	Email       string
}

// ApplyInvoice 按发票号幂等落一张发票：已存在则直接返回原单。
//
// 一单一张的约束单独核对：请求号可以重放，但用户换个请求号对同一笔订单再
// 开一次票必须被拒，否则重复申领绕过幂等。
func ApplyInvoice(tx *gorm.DB, in InvoiceApplyInput) (uint64, error) {
	var existing struct {
		ID uint64 `gorm:"column:id"`
	}
	if found := tx.Table("invoice_request").Where("invoice_no = ?", in.InvoiceNo).Take(&existing); found.Error == nil {
		return existing.ID, nil
	} else if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
		return 0, found.Error
	}
	var duplicate int64
	if err := tx.Table("invoice_request").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", in.OrderID).Count(&duplicate).Error; err != nil {
		return 0, err
	}
	if duplicate > 0 {
		return 0, ErrInvoiceDuplicate
	}
	if err := tx.Table("invoice_request").Create(map[string]any{
		"invoice_no": in.InvoiceNo, "user_id": in.UserID, "biz_type": "charge", "biz_id": in.OrderID,
		"total_cents": in.TotalCents, "invoice_type": in.InvoiceType, "title": in.Title,
		"tax_no": in.TaxNo, "email": in.Email, "review_status": "pending",
	}).Error; err != nil {
		return 0, err
	}
	return LastInsertID(tx)
}

// ListUserInvoiceRequests 分页返回用户自己的发票申请。
func ListUserInvoiceRequests(ctx context.Context, db *gorm.DB, userID uint64, page, pageSize int) (Page[map[string]any], error) {
	out := Page[map[string]any]{Items: []map[string]any{}, Page: page, PageSize: pageSize}
	base := db.WithContext(ctx).Table("invoice_request").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	if err := base.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := base.Select("id, invoice_no, biz_type, biz_id, total_cents, invoice_type, title, tax_no, email, " +
		"review_status, reject_reason, invoice_url, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}
