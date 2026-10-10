package device

import (
	"chargepilot/internal/domain/operations"
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FaultRepo 保存报修状态和事件，事务提交及连接生命周期由调用方管理。
type FaultRepo struct {
	DB *gorm.DB /* 报修所属连接或调用方事务 */
}

// FaultRow 是报修状态变更所需的记录字段。
type FaultRow struct {
	ID         uint64      // 报修单主键
	DeviceID   string      // 报告设备编号，范围核对不接受调用方替换
	Status     FaultStatus // 当前状态：open 待受理 / dispatched 已指派 / fixed 已修复 / closed 已关闭
	AssignedTo *uint64     // 当前处理人 ID，nil 表示还没人接手
}

// scopedFaultQuery 通过现有设备资料限制报告范围；不受限账号保留未知设备的历史报告。
func scopedFaultQuery(ctx context.Context, db *gorm.DB, scope operations.DataScope) *gorm.DB {
	q := db.WithContext(ctx).Table("device_fault_report AS f").Where("f.deleted_at IS NULL")
	if scope.Unrestricted {
		return q
	}
	q = q.Joins("JOIN device_meta AS d ON d.device_id=f.device_id AND d.deleted_at IS NULL")
	return ScopeVendors(scope, ScopeStations(scope, q, "d.station_id"), "d.vendor_id")
}

// ScopedFaultExists 检查当前范围内是否存在报告，不泄漏范围外记录。
func (s FaultRepo) ScopedFaultExists(ctx context.Context, scope operations.DataScope, id uint64) (bool, error) {
	var n int64
	err := scopedFaultQuery(ctx, s.DB, scope).Where("f.id=?", id).Count(&n).Error
	return n > 0, err
}

// ListReports 按范围、状态分页读取报告和总数。
func (s FaultRepo) ListReports(ctx context.Context, scope operations.DataScope, status FaultStatus, page, pageSize int) ([]map[string]any, int64, error) {
	q := scopedFaultQuery(ctx, s.DB, scope)
	if status != "" {
		q = q.Where("f.status=?", status)
	}
	var n int64
	if err := q.Session(&gorm.Session{}).Count(&n).Error; err != nil {
		return nil, 0, err
	}
	rows := []map[string]any{}
	err := q.Select("f.*").Order("f.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, n, err
}

// LockFault 在调用方事务中锁读未删除报告的状态和处理人。
func (s FaultRepo) LockFault(tx *gorm.DB, id uint64) (FaultRow, error) {
	var row FaultRow
	err := tx.Table("device_fault_report").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// FaultEventInput 保存报修变更事件及是否对用户可见，须如实填写操作内容。
// EventType 是 event_type 列的六值词表，不做统一写入校验：用户报修写 reported，
// 后台处理写 dispatched／reassigned／fixed／closed，仓储层不对取值额外拒绝。
type FaultEventInput struct {
	ReportID uint64 // 所属报修单
	ActorID  uint64 // 操作的后台账号
	// event_type 与 FaultStatus 分立：reassigned、migration_baseline 不在报修单状态词表内。
	EventType   FaultEventType
	FromStatus  FaultStatus // 流转前状态；用户报修首条事件写空串，不是 null
	ToStatus    FaultStatus // 流转后状态
	AssignedTo  uint64      // 流转后的处理人
	Note        *string     // 备注，可空
	UserVisible bool        // 是否对用户可见
}

// AppendFaultEvent 在报告状态事务中保存操作人、状态、指派和可见性，状态是否合法由用例判断。
func (s FaultRepo) AppendFaultEvent(tx *gorm.DB, in FaultEventInput) error {
	return tx.Table("device_fault_report_event").Create(map[string]any{
		"report_id": in.ReportID, "actor_id": in.ActorID, "event_type": in.EventType,
		"from_status": in.FromStatus, "to_status": in.ToStatus, "assigned_to": in.AssignedTo,
		"note": in.Note, "user_visible": in.UserVisible,
	}).Error
}

// faultEventSelect 指定历史展示字段，event_id 用别名避免与记录主键混淆。
const faultEventSelect = "id AS event_id, event_type, actor_id, assigned_to, " +
	"from_status, to_status, note, created_at, user_visible"

// ListFaultEvents 按报告分页倒序读取全部历史事件，不过滤后台可见性。
func (s FaultRepo) ListFaultEvents(ctx context.Context, reportID uint64, page, pageSize int) ([]map[string]any, int64, error) {
	query := s.DB.WithContext(ctx).Table("device_fault_report_event").Where("report_id=?", reportID)
	total := int64(0)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []map[string]any{}
	if err := query.Select(faultEventSelect).Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateReport 在调用方事务中更新报修固定字段。
func (FaultRepo) UpdateReport(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Table("device_fault_report").Where("id=?", id).Updates(values).Error
}
