package settlement

import (
	"context"
	"encoding/json"

	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
)

// 退款列表、人工计量复核与发票申请查询。这三组与结算派发同源：
// 钱算完之后的处理都在这里，列表与单条读取一并归入。

// RefundListQuery 是退款列表的筛选条件。
type RefundListQuery struct {
	Status   string
	RefundNo string
	Page     int
	PageSize int
}

// ListRefunds 分页返回未删除的退款记录，并带出该笔退款的最近一条人工复核结论。
// 复核表按 refund_record_id 关联，未复核过的记录复核列为空。
func ListRefunds(ctx context.Context, db *gorm.DB, q RefundListQuery) (persist.Page[map[string]any], error) {
	out := persist.Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	query := db.WithContext(ctx).Table("refund_record r").
		Joins("LEFT JOIN refund_review v ON v.refund_record_id=r.id").
		Where("r.deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("r.status=?", q.Status)
	}
	if q.RefundNo != "" {
		query = query.Where("r.refund_no=?", q.RefundNo)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("r.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// ManualFeeReviewListRow 是人工定价工单的一行。Reviews 由 ListOrderMeterReviews
// 单独回填，gorm:"-" 表示不由本行查询装载。
type ManualFeeReviewListRow struct {
	ChargeOrderID uint64          `json:"charge_order_id" gorm:"column:charge_order_id"` // 计费订单主键，核实记录靠它关联
	OrderNo       string          `json:"order_no" gorm:"column:order_no"`               // 订单号，列表关键词搜索的就是它
	Reason        string          `json:"reason" gorm:"column:reason"`                   // 兜底原因：自动计费为什么没能算出费用
	Status        string          `json:"status" gorm:"column:status"`                   // pending 待核实、resolved 已处置
	SourceJSON    json.RawMessage `json:"source" gorm:"column:source_json"`              // 设备上报的原始计量数据；为空表示设备根本没上报可用数据
	CreatedAt     string          `json:"created_at" gorm:"column:created_at"`
	Reviews       []MeterReview   `json:"reviews" gorm:"-"` // 该订单下的核实记录，按时间倒序
}

// ListManualFeeReviews 分页返回人工定价工单，可按状态与订单号关键字过滤。
func ListManualFeeReviews(ctx context.Context, db *gorm.DB, q persist.PageQuery) (persist.Page[ManualFeeReviewListRow], error) {
	out := persist.Page[ManualFeeReviewListRow]{Items: []ManualFeeReviewListRow{}, Page: q.Page, PageSize: q.PageSize}
	query := db.WithContext(ctx).Table("manual_fee_review")
	if q.Status != "" {
		query = query.Where("status=?", q.Status)
	}
	if q.Keyword != "" {
		query = query.Where("order_no LIKE ? ESCAPE '!'", dbutil.LikePattern(q.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// ListOrderMeterReviews 返回一笔计费订单下的全部核实记录，按 ID 倒序。
func ListOrderMeterReviews(ctx context.Context, db *gorm.DB, chargeOrderID uint64) ([]MeterReview, error) {
	reviews := []MeterReview{}
	err := db.WithContext(ctx).Where("charge_order_id=?", chargeOrderID).Order("id DESC").Find(&reviews).Error
	return reviews, err
}

// FindMeterReview 按核实记录 ID 取一条，并确认它属于指定订单。
// 只按 ID 取会让跨订单的复核 ID 改到别的单据上，所以订单号一并带上条件。
func FindMeterReview(ctx context.Context, db *gorm.DB, reviewID, chargeOrderID uint64) (MeterReview, error) {
	var row MeterReview
	err := db.WithContext(ctx).Where("id=? AND charge_order_id=?", reviewID, chargeOrderID).Take(&row).Error
	return row, err
}

// ListInvoiceRequests 返回全部未删除的开票申请，联表带出人工复核队列的结论。
// 队列结论与申请自身的结论都保留，界面要能显示"还没进队列"与"队列已处理"两种状态。
func ListInvoiceRequests(ctx context.Context, db *gorm.DB) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("invoice_request i").
		Select("i.id AS invoice_request_id,i.invoice_no,i.user_id,i.biz_type,i.biz_id,i.total_cents," +
			"i.invoice_type,i.title,i.tax_no,i.email,i.review_status," +
			"COALESCE(r.review_status,i.review_status) AS queue_review_status," +
			"r.reject_reason AS queue_reject_reason,r.first_reviewer_id,r.second_reviewer_id," +
			"i.reject_reason,COALESCE(r.invoice_url,i.invoice_url) AS invoice_url,i.created_at").
		Joins("LEFT JOIN invoice_admin_review r ON r.invoice_request_id=i.id").
		Where("i.deleted_at IS NULL").Order("i.id DESC").Find(&rows).Error
	return rows, err
}
