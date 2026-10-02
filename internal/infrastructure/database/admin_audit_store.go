package database

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台审计日志的查询，以及数据范围与定价目标核对所需的零散读取。
// 审计只读不写：写入统一走 chargehttp.AdminResourceAudit。

// AuditQuery 是审计列表的筛选条件，零值表示该维度不限。
type AuditQuery struct {
	Module     string
	Action     string
	Actor      string
	TargetType string
	From       *time.Time // 起始时间（含），RFC 3339 解析后的 UTC
	To         *time.Time // 截止时间（含）
}

// AuditRow 映射 audit_log；可空字段用指针保留 NULL 语义——
// 审计里"没有目标"和"目标是空串"是两件事，不能互相顶替。
// 快照列在库中是 JSON，查询时 CAST 为 CHAR 后扫描成文本，
// 直接返回文本而不是让 encoding/json 把 []byte 编成 base64。
type AuditRow struct {
	ID         uint64    `json:"id"`          // 审计记录主键
	ActorID    uint64    `json:"actor_id"`    // 操作的后台账号 ID
	ActorName  string    `json:"actor_name"`  // 操作人登录名（当时的快照）
	Module     string    `json:"module"`      // 所属模块，等于目标类型，如 auth / coupon
	Action     string    `json:"action"`      // 动作，如 login / update / grant
	TargetType *string   `json:"target_type"` // 目标类型；指针，未指定对象时为 null
	TargetID   *string   `json:"target_id"`   // 目标主键（字符串形式）；指针，未指定时为 null
	RequestID  *string   `json:"request_id"`  // 关联请求号；指针，取不到时为 null
	ClientIP   *string   `json:"client_ip"`   // 客户端 IP；指针，取不到时为 null
	BeforeJSON *string   `json:"before_json"` // 变更前快照；指针，新建记录时为 null
	AfterJSON  *string   `json:"after_json"`  // 变更后快照；指针，新建记录时为 null
	CreatedAt  time.Time `json:"created_at"`  // 记录时间
}

// auditSelect 把两份快照 CAST 成文本，保证 int64 之类的列不会被当成
// JSON 扫描失败。
const auditSelect = "id, actor_id, actor_name, module, action, target_type, target_id, request_id, client_ip, " +
	"CAST(before_json AS CHAR) AS before_json, CAST(after_json AS CHAR) AS after_json, created_at"

// ListAudits 分页返回审计记录，按 ID 倒序。计数与列表复用同一套筛选条件。
func ListAudits(ctx context.Context, db *gorm.DB, q AuditQuery, page, pageSize int) (Page[AuditRow], error) {
	out := Page[AuditRow]{Items: []AuditRow{}, Page: page, PageSize: pageSize}
	filtered := func() *gorm.DB {
		query := db.WithContext(ctx).Table("audit_log")
		if q.Module != "" {
			query = query.Where("module = ?", q.Module)
		}
		if q.Action != "" {
			query = query.Where("action = ?", q.Action)
		}
		if q.Actor != "" {
			query = query.Where("actor_name LIKE ?", dbutil.LikePattern(q.Actor))
		}
		if q.TargetType != "" {
			query = query.Where("target_type = ?", q.TargetType)
		}
		if q.From != nil {
			query = query.Where("created_at >= ?", *q.From)
		}
		if q.To != nil {
			query = query.Where("created_at <= ?", *q.To)
		}
		return query
	}
	if err := filtered().Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := filtered().Select(auditSelect).Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out.Items).Error
	return out, err
}

// CountExistingStations 数出给定 ID 中确实存在且未删除的站点数。
// 数据范围保存时用它核对：命中数与提交数量不一致说明范围里混入了不存在的站点。
func CountExistingStations(ctx context.Context, db *gorm.DB, ids []uint64) (int64, error) {
	var found int64
	err := db.WithContext(ctx).Table("station").Where("id IN ? AND deleted_at IS NULL", ids).Count(&found).Error
	return found, err
}

// DeviceVendorID 返回设备所属的厂商 ID。设备不存在或不属于该站点时按未找到处理。
func DeviceVendorID(ctx context.Context, db *gorm.DB, stationID uint64, deviceID string) (uint64, error) {
	var row struct{ VendorID uint64 }
	err := db.WithContext(ctx).Table("device_meta").Select("vendor_id").
		Where("station_id=? AND device_id=? AND deleted_at IS NULL", stationID, deviceID).Take(&row).Error
	return row.VendorID, err
}

// OrderProcessIdentity 是订单处理视图需要的订单身份字段。
type OrderProcessIdentity struct {
	ID       uint64
	OrderNo  string
	DeviceID string
	PortNo   uint8
}

// FindOrderIdentity 按主键取订单身份，订单不存在或已软删除时返回 gorm.ErrRecordNotFound。
func FindOrderIdentity(ctx context.Context, db *gorm.DB, id uint64) (OrderProcessIdentity, error) {
	var row OrderProcessIdentity
	err := db.WithContext(ctx).Table("charge_order").
		Select("id, order_no, device_id, port_no").
		Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// FindStation 按主键取一个未删除的站点。
func FindStation(ctx context.Context, db *gorm.DB, id uint64) (Station, error) {
	var row Station
	err := db.WithContext(ctx).Table("station").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// LockStationForUpdate 在行锁下取站点，返回更新前的完整快照供审计留痕。
func LockStationForUpdate(tx *gorm.DB, id uint64) (Station, error) {
	var row Station
	err := tx.Table("station").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// UpdateStation 写入站点的可编辑字段。分账模板不在这里改，只能走独立的绑定接口。
func UpdateStation(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Model(&Station{}).Where("id = ?", id).Updates(values).Error
}
