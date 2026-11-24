package settlement

import (
	"context"
	"errors"
	"wattharbor/internal/domain/money"
	orderdomain "wattharbor/internal/domain/order"
	"wattharbor/internal/persist"

	"gorm.io/gorm"
)

// 用户申请持久化与运营复核共用 invoice_request；复核写入位于 invoice_repo.go。
// 开票额来自费用回执，客户端只提交票种及抬头等内容，不决定金额。

// InvoiceApplyOrder 是申请发票时要核对的订单信息。
type InvoiceApplyOrder struct {
	ID     uint64                  // 充电订单主键
	UserID uint64                  // 订单所属用户 ID
	Status orderdomain.OrderStatus // 同一订单生命周期状态，供用例判断是否可申请
}

// FindInvoiceableOrder 按订单号和用户读取未删除订单；缺失返回 gorm.ErrRecordNotFound。
func FindInvoiceableOrder(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (InvoiceApplyOrder, error) {
	var row struct {
		ID     uint64                  `gorm:"column:id"`      // 同一订单主键
		UserID uint64                  `gorm:"column:user_id"` // 所属用户 ID
		Status orderdomain.OrderStatus `gorm:"column:status"`  // 同一订单生命周期状态
	}
	err := db.WithContext(ctx).Table("charge_order").Select("id, user_id, status").
		Where("order_no = ? AND user_id = ? AND deleted_at IS NULL", orderNo, userID).Take(&row).Error
	return InvoiceApplyOrder{ID: row.ID, UserID: row.UserID, Status: row.Status}, err
}

// ErrInvoiceDuplicate 表示订单已有申请，或同发票号的申请身份、金额及内容不一致。
var ErrInvoiceDuplicate = errors.New("该订单已申请过发票")

// InvoiceApplyInput 是一次发票申请。TotalCents 由调用方从费用回执读出后传入，
// 不是客户端提交的值。
type InvoiceApplyInput struct {
	InvoiceNo   string      // 稳定申请号，同号必须重放相同身份和内容
	UserID      uint64      // 申请用户，必须为订单所有者
	OrderID     uint64      // 申请来源充电订单主键
	TotalCents  int64       // 已确认费用回执的实际开票额，分
	InvoiceType InvoiceType // 票种，由用例校验支持范围（invoice_request.invoice_type）
	Title       string      // 发票抬头
	TaxNo       string      // 税号，可按票种为空
	Email       string      // 通知邮箱，未填写为空字符串
}

// ApplyInvoice 在调用方事务中登记发票申请，重放须核对申请人、订单、费用和全部内容。
// 使用 READ COMMITTED 并先由 order.LockInvoiceSourceInTx 锁定本人订单，防止同单不同请求号重复申请。
func ApplyInvoice(tx *gorm.DB, in InvoiceApplyInput) (uint64, error) {
	var existing struct {
		ID          uint64      `gorm:"column:id"`           // 申请主键
		UserID      uint64      `gorm:"column:user_id"`      // 申请人
		BizType     string      `gorm:"column:biz_type"`     // 业务类型，必须为 charge
		BizID       uint64      `gorm:"column:biz_id"`       // 订单 ID
		TotalCents  int64       `gorm:"column:total_cents"`  // 开票额，分
		InvoiceType InvoiceType `gorm:"column:invoice_type"` // 票种
		Title       string      `gorm:"column:title"`        // 抬头
		TaxNo       string      `gorm:"column:tax_no"`       // 税号，未填写为空字符串
		Email       string      `gorm:"column:email"`        // 通知邮箱，未填写为空字符串
	}
	if found := tx.Table("invoice_request").Where("invoice_no = ?", in.InvoiceNo).Take(&existing); found.Error == nil {
		if existing.UserID != in.UserID || existing.BizType != "charge" || existing.BizID != in.OrderID || existing.TotalCents != in.TotalCents || existing.InvoiceType != in.InvoiceType || existing.Title != in.Title || existing.TaxNo != in.TaxNo || existing.Email != in.Email {
			return 0, ErrInvoiceDuplicate
		}
		return existing.ID, nil
	} else if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
		return 0, found.Error
	}
	var duplicate int64
	if err := tx.Table("invoice_request").
		Where("biz_type = ? AND biz_id = ? AND deleted_at IS NULL", money.PaymentBizCharge, in.OrderID).Count(&duplicate).Error; err != nil {
		return 0, err
	}
	if duplicate > 0 {
		return 0, ErrInvoiceDuplicate
	}
	if err := tx.Table("invoice_request").Create(map[string]any{
		"invoice_no": in.InvoiceNo, "user_id": in.UserID, "biz_type": "charge", "biz_id": in.OrderID,
		"total_cents": in.TotalCents, "invoice_type": in.InvoiceType, "title": in.Title,
		"tax_no": in.TaxNo, "email": in.Email, "review_status": InvoiceRequestPending,
	}).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// ListUserInvoiceRequests 分页倒序读取本人未删除的发票申请。
func ListUserInvoiceRequests(ctx context.Context, db *gorm.DB, userID uint64, page, pageSize int) (persist.Page[map[string]any], error) {
	out := persist.Page[map[string]any]{Items: []map[string]any{}, Page: page, PageSize: pageSize}
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

// LoadInvoiceFee 读取订单费用回执，缺失返回 gorm.ErrRecordNotFound。
func LoadInvoiceFee(ctx context.Context, db *gorm.DB, orderID uint64) (ChargeFeeRecord, error) {
	var receipt ChargeFeeRecord
	err := db.WithContext(ctx).Where("charge_order_id=?", orderID).Take(&receipt).Error
	return receipt, err
}
