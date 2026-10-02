package http

import (
	"errors"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
)

// 开票申请与我的发票。
func (a UserAccountAPI) myInvoices(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	base := a.UserDB.WithContext(c.Request.Context()).Table("invoice_request").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "发票记录暂时无法读取", nil)
		return
	}
	rows := []map[string]any{}
	if err := base.Select("id, invoice_no, biz_type, biz_id, total_cents, invoice_type, title, tax_no, email, review_status, reject_reason, invoice_url, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "发票记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// applyInvoice 为客户自己某一笔已结算的订单申请发票。
// 金额取自订单，绝不取自请求。
func (a UserAccountAPI) applyInvoice(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		OrderNo     string `json:"order_no"`
		InvoiceType string `json:"invoice_type"`
		Title       string `json:"title"`
		TaxNo       string `json:"tax_no"`
		Email       string `json:"email"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "发票申请无效")
		return
	}
	if uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.OrderNo) == "" {
		httpapi.BadRequest(c, "请提供 UUID 请求号与订单号")
		return
	}
	// 库里枚举存的是 normal / vat_special；小程序发过来的也是这两个名字。
	if !oneOfStatus(in.InvoiceType, "normal vat_special") {
		httpapi.BadRequest(c, "发票类型无效")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || len([]rune(title)) > 128 {
		httpapi.BadRequest(c, "发票抬头无效")
		return
	}
	if len([]rune(in.TaxNo)) > 32 {
		httpapi.BadRequest(c, "税号过长")
		return
	}
	if len(in.Email) > 128 || (in.Email != "" && !strings.Contains(in.Email, "@")) {
		httpapi.BadRequest(c, "邮箱格式无效")
		return
	}
	ctx := c.Request.Context()
	var order struct {
		ID     uint64 `gorm:"column:id"`
		UserID uint64 `gorm:"column:user_id"`
		Status string `gorm:"column:status"`
	}
	err := a.UserDB.WithContext(ctx).Table("charge_order").
		Select("id, user_id, status").
		Where("order_no = ? AND user_id = ? AND deleted_at IS NULL", in.OrderNo, userID).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "订单不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return
	}
	// 仅已结算的充电订单可申请发票。
	if order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded" {
		httpapi.Write(c, 409, 2009, "订单尚未完成，暂不能申请发票", nil)
		return
	}
	// 开票金额取自计费回执，
	// 与订单视图同源，绝不取自计费任务从不写入的列。
	receipt := orderstore.ChargeFeeRecord{}
	if err := a.UserDB.WithContext(ctx).Where("charge_order_id = ?", order.ID).Take(&receipt).Error; err != nil {
		httpapi.Write(c, 409, 2009, "订单尚未计费，暂不能申请发票", nil)
		return
	}
	_, _, totalCents, ok := receipt.Fees()
	if !ok || totalCents <= 0 {
		httpapi.Write(c, 409, 2009, "订单金额为零，暂不能申请发票", nil)
		return
	}
	invoiceNo := "INV" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	var id uint64
	err = a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			ID uint64 `gorm:"column:id"`
		}
		if found := tx.Table("invoice_request").Where("invoice_no = ?", invoiceNo).Take(&existing); found.Error == nil {
			id = existing.ID
			return nil
		} else if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		// 一单一张发票，防止客户把同一笔充电重复申领。
		var duplicate int64
		if err := tx.Table("invoice_request").
			Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", order.ID).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errInvoiceDuplicate
		}
		if err := tx.Table("invoice_request").Create(map[string]any{
			"invoice_no": invoiceNo, "user_id": userID, "biz_type": "charge", "biz_id": order.ID,
			"total_cents": totalCents, "invoice_type": in.InvoiceType, "title": title,
			"tax_no": strings.TrimSpace(in.TaxNo), "email": strings.TrimSpace(in.Email), "review_status": "pending",
		}).Error; err != nil {
			return err
		}
		return tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error
	})
	if errors.Is(err, errInvoiceDuplicate) {
		httpapi.Write(c, 409, 2009, "该订单已申请过发票", nil)
		return
	}
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"invoice_no": invoiceNo, "id": id, "review_status": "pending", "total_cents": totalCents})
}

// ---- 共享工具 ----

var (
	ErrInsufficientBalance = errors.New("可用余额不足")
	errWalletFrozen        = errors.New("钱包已冻结")
	errWalletIdentity      = errors.New("账号身份不可用")
	errInvoiceDuplicate    = errors.New("订单已申请发票")
)

func readPaging(c *gin.Context) (int, int, bool) {
	return readPagingDefault(c, 20)
}
