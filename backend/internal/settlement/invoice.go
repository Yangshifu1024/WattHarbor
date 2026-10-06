package settlement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	orderrepo "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvoiceInput       = errors.New("invalid invoice application") // 发票申请内容无效
	ErrInvoiceOrderLookup = errors.New("invoice order lookup failed") // 订单归属读取失败
	ErrInvoiceUnfinished  = errors.New("invoice order unfinished")    // 订单尚未完成
	ErrInvoiceUnbilled    = errors.New("invoice order unbilled")      // 原计费回执无法取得
	ErrInvoiceZero        = errors.New("invoice fee is zero")         // 原费用未确定或非正数
)

// InvoiceService 拥有用户订单归属、费用来源与发票申请事务。
type InvoiceService struct {
	DB *gorm.DB /* central 订单及发票连接，由进程管理 */
}

// List 在ctx下按userID及原page／pageSize读取本人发票申请，返回原页或读取／取消错误。
// 不提交或复核申请，不扩展为其它用户记录，保留原排序及可空字段。
func (s InvoiceService) List(ctx context.Context, userID uint64, page, pageSize int) (persist.Page[map[string]any], error) {
	return ListUserInvoiceRequests(ctx, s.DB, userID, page, pageSize)
}

// InvoiceInput 保持公开申请内容，金额始终由原结算回执提供。
type InvoiceInput struct {
	RequestID   string `json:"request_id"`   // 客户端 UUID，派生原发票号
	OrderNo     string `json:"order_no"`     // 本用户原订单号
	InvoiceType string `json:"invoice_type"` // normal 或 vat_special
	Title       string `json:"title"`        // 发票抬头，规范化后 1–128 字
	TaxNo       string `json:"tax_no"`       // 税号，最多 32 字
	Email       string `json:"email"`        // 原通知邮箱，最多 128 字节
}

// InvoiceApplication 是原申请回执，金额单位分，ID 保持原公开整数契约。
type InvoiceApplication struct {
	InvoiceNo    string `json:"invoice_no"`    // 原请求号派生的发票编号
	ID           uint64 `json:"id"`            // 发票申请主键
	ReviewStatus string `json:"review_status"` // 原申请结果固定 pending
	TotalCents   int64  `json:"total_cents"`   // 原费用回执的确定总额，分
}

// Create 在 ctx 下为 userID 的 in 申请发票，不接受客户端金额。
// 保持原先归属和结算证据检查顺序，按原发票号重放、一单一张；失败不留下申请。
// 返回申请回执或输入、归属、未结算、重复及存储错误，不表示审核或已开票。
func (s InvoiceService) Create(ctx context.Context, userID uint64, in InvoiceInput) (InvoiceApplication, error) {
	title := strings.TrimSpace(in.Title)
	if userID == 0 || uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.OrderNo) == "" || (in.InvoiceType != "normal" && in.InvoiceType != "vat_special") || title == "" || len([]rune(title)) > 128 || len([]rune(in.TaxNo)) > 32 || len(in.Email) > 128 || (in.Email != "" && !strings.Contains(in.Email, "@")) {
		return InvoiceApplication{}, ErrInvoiceInput
	}
	order, err := FindInvoiceableOrder(ctx, s.DB, in.OrderNo, userID)
	if err != nil {
		return InvoiceApplication{}, fmt.Errorf("%w: %w", ErrInvoiceOrderLookup, err)
	}
	if order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded" {
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
		if status != "completed" && status != "refunding" && status != "refunded" {
			return ErrInvoiceUnfinished
		}
		id, err = ApplyInvoice(tx, InvoiceApplyInput{InvoiceNo: no, UserID: userID, OrderID: order.ID, TotalCents: total, InvoiceType: in.InvoiceType, Title: title, TaxNo: strings.TrimSpace(in.TaxNo), Email: strings.TrimSpace(in.Email)})
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return InvoiceApplication{InvoiceNo: no, ID: id, ReviewStatus: "pending", TotalCents: total}, err
}
