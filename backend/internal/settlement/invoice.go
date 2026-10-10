package settlement

import (
	orderdomain "chargepilot/internal/domain/order"
	orderrepo "chargepilot/internal/order"
	"chargepilot/internal/persist"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvoiceInput       = errors.New("invalid invoice application") // 发票申请内容无效
	ErrInvoiceOrderLookup = errors.New("invoice order lookup failed") // 订单归属读取失败
	ErrInvoiceUnfinished  = errors.New("invoice order unfinished")    // 订单尚未完成
	ErrInvoiceUnbilled    = errors.New("invoice order unbilled")      // 计费回执无法取得
	ErrInvoiceZero        = errors.New("invoice fee is zero")         // 已确认费用未确定或非正数
)

// InvoiceService 拥有用户订单归属、费用来源与发票申请事务。
type InvoiceService struct {
	DB *gorm.DB /* central 订单及发票连接，由进程管理 */
}

// List 分页读取本人发票申请，保留查询排序和可空字段。
func (s InvoiceService) List(ctx context.Context, userID uint64, page, pageSize int) (persist.Page[map[string]any], error) {
	return ListUserInvoiceRequests(ctx, s.DB, userID, page, pageSize)
}

// InvoiceInput 是发票申请内容，金额由已保存的结算回执提供。
type InvoiceInput struct {
	RequestID   string `json:"request_id"`   // 客户端 UUID，派生发票号
	OrderNo     string `json:"order_no"`     // 本用户同一订单号
	InvoiceType string `json:"invoice_type"` // normal 或 vat_special
	Title       string `json:"title"`        // 发票抬头，规范化后 1–128 字
	TaxNo       string `json:"tax_no"`       // 税号，最多 32 字
	Email       string `json:"email"`        // 通知邮箱，最多 128 字节
}

// InvoiceApplication 返回申请结果，金额单位分，ID 使用整数。
type InvoiceApplication struct {
	InvoiceNo    string `json:"invoice_no"`    // 首次请求号派生的发票编号
	ID           uint64 `json:"id"`            // 发票申请主键
	ReviewStatus string `json:"review_status"` // 申请结果固定 pending
	TotalCents   int64  `json:"total_cents"`   // 已确认费用回执的确定总额，分
}

// Create 核对本人订单和结算回执后申请发票，金额不由客户端决定。
// 一单只允许一份申请，同号同内容重放；成功只表示已申请，不代表已审核或开票。
func (s InvoiceService) Create(ctx context.Context, userID uint64, in InvoiceInput) (InvoiceApplication, error) {
	title := strings.TrimSpace(in.Title)
	if userID == 0 || uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.OrderNo) == "" || (in.InvoiceType != "normal" && in.InvoiceType != "vat_special") || title == "" || len([]rune(title)) > 128 || len([]rune(in.TaxNo)) > 32 || len(in.Email) > 128 || (in.Email != "" && !strings.Contains(in.Email, "@")) {
		return InvoiceApplication{}, ErrInvoiceInput
	}
	order, err := FindInvoiceableOrder(ctx, s.DB, in.OrderNo, userID)
	if err != nil {
		return InvoiceApplication{}, fmt.Errorf("%w: %w", ErrInvoiceOrderLookup, err)
	}
	if !invoiceableStatus(order.Status) {
		return InvoiceApplication{}, ErrInvoiceUnfinished
	}
	receipt, err := LoadInvoiceFee(ctx, s.DB, order.ID)
	if err != nil {
		return InvoiceApplication{}, fmt.Errorf("%w: %w", ErrInvoiceUnbilled, err)
	}
	_, _, total, ok := receipt.Fees()
	if !ok || total <= 0 {
		return InvoiceApplication{}, ErrInvoiceZero
	}
	no := "INV" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	var id uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status, err := orderrepo.LockInvoiceSourceInTx(tx, order.ID, userID)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvoiceOrderLookup, err)
		}
		if !invoiceableStatus(status) {
			return ErrInvoiceUnfinished
		}
		id, err = ApplyInvoice(tx, InvoiceApplyInput{InvoiceNo: no, UserID: userID, OrderID: order.ID, TotalCents: total, InvoiceType: in.InvoiceType, Title: title, TaxNo: strings.TrimSpace(in.TaxNo), Email: strings.TrimSpace(in.Email)})
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return InvoiceApplication{InvoiceNo: no, ID: id, ReviewStatus: "pending", TotalCents: total}, err
}

// invoiceableStatus 判断订单是否已可开票：已完成、已受理退款或已退款都允许，
// 其余状态说明尚未产生最终可结算的计费凭据。发票申请与计费任务共用该判定，
// 两处必须一致，否则同一订单会在两条链路上得到相反结论。
func invoiceableStatus(status orderdomain.OrderStatus) bool {
	return status == orderdomain.StatusCompleted || status == orderdomain.StatusRefunding || status == orderdomain.StatusRefunded
}
