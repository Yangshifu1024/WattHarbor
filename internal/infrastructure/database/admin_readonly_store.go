package database

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台剩余的四个列表与若干单条读取：退款、人工计量工单、开票、设备详情。
// 它们的共同点是"一张主表带一次联表或一次回填"，没有各自的写入语义。

// RefundListQuery 是退款列表的筛选条件。
type RefundListQuery struct {
	Status   string
	RefundNo string
	Page     int
	PageSize int
}

// ListRefunds 分页返回未删除的退款记录，并带出该笔退款的最近一条人工复核结论。
// 复核表按 refund_record_id 关联，未复核过的记录复核列为空。
func ListRefunds(ctx context.Context, db *gorm.DB, q RefundListQuery) (Page[map[string]any], error) {
	out := Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
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
func ListManualFeeReviews(ctx context.Context, db *gorm.DB, q PageQuery) (Page[ManualFeeReviewListRow], error) {
	out := Page[ManualFeeReviewListRow]{Items: []ManualFeeReviewListRow{}, Page: q.Page, PageSize: q.PageSize}
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

// OrderCardPurchase 是订单用卡充值时的实际购买内容。
type OrderCardPurchase struct {
	PaidCents        int64  `json:"paid_cents"`        // 实付金额，单位分
	PurchasedMinutes uint16 `json:"purchased_minutes"` // 实际购买的时长，单位分钟
	MaxMinutes       uint16 `json:"max_minutes"`       // 本单允许的最长充电时长，单位分钟
}

// OrderCardCharge 是一次用卡购买。ChargeOrderID 为 0 表示该订单没用卡。
type OrderCardCharge struct {
	ChargeOrderID uint64
	OrderCardPurchase
}

// FindOrderCardCharge 返回订单的用卡购买记录。取不到时 ChargeOrderID 为 0，
// 调用方据此判断"这张单没用卡"，而不是当成查询失败。
func FindOrderCardCharge(ctx context.Context, db *gorm.DB, orderID uint64) (OrderCardCharge, error) {
	var card OrderCardCharge
	err := db.WithContext(ctx).Table("card_charge").
		Select("charge_order_id,paid_cents,purchased_minutes,max_minutes").
		Where("charge_order_id = ?", orderID).Find(&card).Error
	return card, err
}

// ListPricingSnapshots 一次读取一批订单的计价快照。订单列表用它回填方案与
// 套餐名，避免为每条订单单查一次库。
func ListPricingSnapshots(ctx context.Context, db *gorm.DB, orderIDs []uint64) ([]ChargePricingSnapshotRecord, error) {
	rows := []ChargePricingSnapshotRecord{}
	err := db.WithContext(ctx).Select("charge_order_id,pricing_snapshot").
		Where("charge_order_id IN ?", orderIDs).Find(&rows).Error
	return rows, err
}

// FindPricingSnapshot 取单个订单的计价快照。订单详情用它回显下单时冻结的
// 方案与套餐——读的是冻结值，不是当前的计费模板。
func FindPricingSnapshot(ctx context.Context, db *gorm.DB, orderID uint64) (ChargePricingSnapshotRecord, error) {
	var row ChargePricingSnapshotRecord
	err := db.WithContext(ctx).Select("pricing_snapshot").
		Where("charge_order_id = ?", orderID).Find(&row).Error
	return row, err
}

// FindDeviceByID 在站点与厂商数据范围内按设备号取设备详情。
// 范围条件与列表共用同一组 Scope 函数，避免详情比列表看得宽。
func FindDeviceByID(ctx context.Context, db *gorm.DB, scope operations.DataScope, deviceID string) (Device, error) {
	var row Device
	query := ScopeVendors(scope, ScopeStations(scope, DeviceQuery(ctx, db), "d.station_id"), "d.vendor_id")
	err := query.Select(DeviceColumns).Where("d.device_id = ?", deviceID).Take(&row).Error
	return row, err
}

// LockDeviceByID 在行锁下按设备号取设备，写操作前用它核对归属。
func LockDeviceByID(ctx context.Context, tx *gorm.DB, scope operations.DataScope, deviceID string) (Device, error) {
	var row Device
	query := ScopeVendors(scope, ScopeStations(scope, DeviceQuery(ctx, tx), "d.station_id"), "d.vendor_id")
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Select(DeviceColumns).
		Where("d.device_id=?", deviceID).Take(&row).Error
	return row, err
}

// SaveDeviceStatus 写入设备的运营状态与更新时间。
func SaveDeviceStatus(tx *gorm.DB, id uint64, status string, updatedAt time.Time) error {
	return tx.Table("device_meta").Where("id=?", id).
		Updates(map[string]any{"status": status, "updated_at": updatedAt}).Error
}

// SaveDeviceMetadata 写入设备可编辑的元信息。updated_at 由调用方按
// NextDeviceUpdateTime 推算后传入，保证同一秒内的连续修改仍然单调递增。
func SaveDeviceMetadata(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Table("device_meta").Where("id=?", id).Updates(values).Error
}
