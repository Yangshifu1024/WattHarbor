package database

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台的用户反馈与设备报修工单。两者的表结构相似：一张主表加一张
// 事件流水表，因此共用这里的读写入口，但状态机的判定留在 domain/devices。
//
// 反馈：feedback，pending 待处理 / processed 已回复 / closed 已关闭。
// 报修：device_fault_report，open 待受理 / dispatched 已指派 / fixed 已修复 /
// closed 已关闭；每次流转在 device_fault_report_event 留一条用户可见的记录。

// CaseListSpec 声明一类工单的列表来源。表名不同但列结构一致，
// 因此共用一个查询实现。
type CaseListSpec struct {
	Table string // 主表名
}

// 允许后台列出的工单类型。未登记的类型一律拒绝查询。
var caseRegistries = map[string]CaseListSpec{
	"feedback":            {Table: "feedback"},
	"device_fault_report": {Table: "device_fault_report"},
}

// CaseListSpecOf 按类型取列表声明，第二个返回值为 false 表示未登记。
func CaseListSpecOf(kind string) (CaseListSpec, bool) {
	spec, ok := caseRegistries[kind]
	return spec, ok
}

// ListCases 分页返回未删除的工单，可按状态过滤，按 ID 倒序。
// 返回通用行集合：两张表的列不完全相同，处理器按需取用与归一化。
func (s CaseStore) ListCases(ctx context.Context, spec CaseListSpec, status string, page, pageSize int) ([]map[string]any, int64, error) {
	query := s.DB.WithContext(ctx).Table(spec.Table).Where("deleted_at IS NULL")
	if status != "" {
		query = query.Where("status=?", status)
	}
	total := int64(0)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []map[string]any{}
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// CaseStore 是反馈与报修工单的读写入口。
type CaseStore struct{ DB *gorm.DB }

// CaseStatus 是在行锁下读出的工单状态，供状态机判定使用。
type CaseStatus struct {
	Status string // 反馈：pending/processed/closed；报修：open/dispatched/fixed/closed
}

// LockCaseStatus 在行锁下读出工单当前状态。状态机判定必须在锁内完成，
// 否则并发提交会各自基于过期状态作出判定。
func (s CaseStore) LockCaseStatus(tx *gorm.DB, table string, id uint64) (CaseStatus, error) {
	var row struct{ Status string }
	err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return CaseStatus{Status: row.Status}, err
}

// UpdateCase 写入工单的状态与相关列。
func (s CaseStore) UpdateCase(tx *gorm.DB, table string, id uint64, values map[string]any) error {
	return tx.Table(table).Where("id=?", id).Updates(values).Error
}

// FaultRow 是报修状态流转所需的最小字段投影。
type FaultRow struct {
	ID         uint64  // 报修单主键
	Status     string  // 当前状态：open 待受理 / dispatched 已指派 / fixed 已修复 / closed 已关闭
	AssignedTo *uint64 // 当前处理人 ID，nil 表示还没人接手
}

// LockFault 在行锁下读出报修单的流转所需字段。
func (s CaseStore) LockFault(tx *gorm.DB, id uint64) (FaultRow, error) {
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

// AppendFaultEvent 记一次报修流转事件。
func (s CaseStore) AppendFaultEvent(tx *gorm.DB, in FaultEventInput) error {
	return tx.Table("device_fault_report_event").Create(map[string]any{
		"report_id": in.ReportID, "actor_id": in.ActorID, "event_type": in.EventType,
		"from_status": in.FromStatus, "to_status": in.ToStatus, "assigned_to": in.AssignedTo,
		"note": in.Note, "user_visible": in.UserVisible,
	}).Error
}

// FaultExists 确认报修单存在且未软删除。历史接口先确认工单存在，
// 免得"工单不存在"和"还没有历史记录"返回同样的结果。
func (s CaseStore) FaultExists(ctx context.Context, db *gorm.DB, id uint64) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Table("device_fault_report").
		Where("id=? AND deleted_at IS NULL", id).Count(&n).Error
	return n > 0, err
}

// faultEventSelect 是报修历史要返回的列。event_id 是别名列，
// 避免与主键 id 混淆。
const faultEventSelect = "id AS event_id, event_type, actor_id, assigned_to, " +
	"from_status, to_status, note, created_at, user_visible"

// ListFaultEvents 分页返回一张报修单的流转事件，按 ID 倒序。
func (s CaseStore) ListFaultEvents(ctx context.Context, db *gorm.DB, reportID uint64, page, pageSize int) ([]map[string]any, int64, error) {
	query := db.WithContext(ctx).Table("device_fault_report_event").Where("report_id=?", reportID)
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
