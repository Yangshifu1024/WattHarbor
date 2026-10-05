package device

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FaultRepo 拥有报修状态与事件表，进程管理连接；调用方事务不由 repo 提交。
type FaultRepo struct {
	DB *gorm.DB /* 报修所属连接或调用方事务 */
}

// FaultRow 是报修状态流转所需的最小字段投影。
type FaultRow struct {
	ID         uint64  // 报修单主键
	DeviceID   string  // 原报告设备身份，范围核对不接受调用方替换
	Status     string  // 当前状态：open 待受理 / dispatched 已指派 / fixed 已修复 / closed 已关闭
	AssignedTo *uint64 // 当前处理人 ID，nil 表示还没人接手
}

// scopedFaultQuery 仅在所属repo按 scope 和原设备元数据约束报告；不受限时保留未知设备的历史报告。
// 返回内部查询，不向用例暴露builder，不写设备资料或自行取得业务权限。
func scopedFaultQuery(ctx context.Context, db *gorm.DB, scope operations.DataScope) *gorm.DB {
	q := db.WithContext(ctx).Table("device_fault_report AS f").Where("f.deleted_at IS NULL")
	if scope.Unrestricted {
		return q
	}
	q = q.Joins("JOIN device_meta AS d ON d.device_id=f.device_id AND d.deleted_at IS NULL")
	return ScopeVendors(scope, ScopeStations(scope, q, "d.station_id"), "d.vendor_id")
}

// ScopedFaultExists 在 ctx 下读取 scope 可见的 id 存在性，返回事实或存储错误，不泄漏范围外的报告。
func (s FaultRepo) ScopedFaultExists(ctx context.Context, scope operations.DataScope, id uint64) (bool, error) {
	var n int64
	err := scopedFaultQuery(ctx, s.DB, scope).Where("f.id=?", id).Count(&n).Error
	return n > 0, err
}

// ListReports 在 ctx 下按 scope、status、page／pageSize 返回报告及总数；字段和顺序沿用原投影。
func (s FaultRepo) ListReports(ctx context.Context, scope operations.DataScope, status string, page, pageSize int) ([]map[string]any, int64, error) {
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

// LockFault 在调用方 tx 下按 id 当前读锁定未删除报修，返回原状态／处理人或缺失／读取错误，不提交。
func (s FaultRepo) LockFault(tx *gorm.DB, id uint64) (FaultRow, error) {
	var row FaultRow
	err := tx.Table("device_fault_report").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// FaultEventInput 是一次报修流转要写入的事件。事件对用户可见，
// 所以每个字段都要如实填写，不留空来暗示"没发生过"。
type FaultEventInput struct {
	ReportID    uint64  // 所属报修单
	ActorID     uint64  // 操作的后台账号
	EventType   string  // dispatched / reassigned / fixed / closed
	FromStatus  string  // 流转前状态
	ToStatus    string  // 流转后状态
	AssignedTo  uint64  // 流转后的处理人
	Note        *string // 备注，可空
	UserVisible bool    // 是否对用户可见
}

// AppendFaultEvent 在调用方 tx 下保存 in 的原报修、操作人、流转、指派及可见性，返回写入错误。
// 事件与主状态须同事务；本方法不判定状态机或自行提交。
func (s FaultRepo) AppendFaultEvent(tx *gorm.DB, in FaultEventInput) error {
	return tx.Table("device_fault_report_event").Create(map[string]any{
		"report_id": in.ReportID, "actor_id": in.ActorID, "event_type": in.EventType,
		"from_status": in.FromStatus, "to_status": in.ToStatus, "assigned_to": in.AssignedTo,
		"note": in.Note, "user_visible": in.UserVisible,
	}).Error
}

// faultEventSelect 是报修历史要返回的列。event_id 是别名列，
// 避免与主键 id 混淆。
const faultEventSelect = "id AS event_id, event_type, actor_id, assigned_to, " +
	"from_status, to_status, note, created_at, user_visible"

// ListFaultEvents 在 ctx 下按 reportID 和已校验 page/pageSize 返回事件、总数或读取错误。
// 原事件按 ID 倒序，后台保留所有可见性事实；无写入或指派。
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

// UpdateReport 在调用方 tx 下将 id 报修写为 values 所属字段，返回写入错误，不提交或选择其他表。
func (FaultRepo) UpdateReport(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Table("device_fault_report").Where("id=?", id).Updates(values).Error
}
