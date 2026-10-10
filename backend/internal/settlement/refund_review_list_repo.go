package settlement

import (
	"context"
	"encoding/json"

	"chargepilot/internal/persist"
	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件读取退款、计量核实和发票队列，并提供审核所需锁读。

// RefundListQuery 是退款列表的筛选条件。
type RefundListQuery struct {
	Status   string // 退款状态（refund_record.status 全量取值），空值不筛选；由 HTTP 白名单校验后传入
	RefundNo string // 完整退款单号，空值不筛选
	Page     int    // 已规范化的页码，从 1 开始
	PageSize int    // 已规范化的每页条数
}

// ListRefunds 分页倒序读取未删除退款和最近人工复核；未经复核时相关字段为空。
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

// ManualFeeReviewListRow 是人工定价工单的一行。Reviews 由用例按本页订单批量回填。
type ManualFeeReviewListRow struct {
	ChargeOrderID uint64          `json:"charge_order_id" gorm:"column:charge_order_id"` // 计费订单主键，核实记录靠它关联
	OrderNo       string          `json:"order_no" gorm:"column:order_no"`               // 订单号，列表关键词搜索的就是它
	Reason        string          `json:"reason" gorm:"column:reason"`                   // 兜底因：自动计费为什么没能算出费用
	Status        string          `json:"status" gorm:"column:status"`                   // pending 待核实、resolved 已处置
	SourceJSON    json.RawMessage `json:"source" gorm:"column:source_json"`              // 设备上报的原始计量数据；为空表示设备根本没上报可用数据
	CreatedAt     string          `json:"created_at" gorm:"column:created_at"`           // 工单创建时间，保留数据库时间文本
	Reviews       []MeterReview   `json:"reviews" gorm:"-"`                              // 该订单下的核实记录，按时间倒序
}

// ListManualFeeReviews 按状态、订单号关键词分页倒序读取人工定价工单，不补充核实记录。
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

// ListMeterReviewsByOrder 批量读取核实记录，按订单分组、ID 倒序；空页不查询。
func ListMeterReviewsByOrder(ctx context.Context, db *gorm.DB, orderIDs []uint64) (map[uint64][]MeterReview, error) {
	out := map[uint64][]MeterReview{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	reviews := []MeterReview{}
	err := db.WithContext(ctx).Where("charge_order_id IN ?", orderIDs).Order("id DESC").Find(&reviews).Error
	if err != nil {
		return nil, err
	}
	for _, row := range reviews {
		out[row.ChargeOrderID] = append(out[row.ChargeOrderID], row)
	}
	return out, nil
}

// FindMeterReview 在已锁工单的事务中锁读所属核实记录，保持工单→核实的锁序。
// 身份不符或记录缺失返回 gorm.ErrRecordNotFound。
func FindMeterReview(ctx context.Context, db *gorm.DB, reviewID, chargeOrderID uint64) (MeterReview, error) {
	var row MeterReview
	err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND charge_order_id=?", reviewID, chargeOrderID).Take(&row).Error
	return row, err
}

// ListInvoiceRequests 按申请 ID 倒序读取发票申请及人工队列结论，两类结论分别保留。
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
