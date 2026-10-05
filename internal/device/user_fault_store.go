package device

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"

	"gorm.io/gorm"
)

// 用户侧的设备报障。运营侧的报修表访问在 fault_repo.go，这里是用户
// 提交与查看：用户只能看到自己提交的，以及流转中标记为用户可见的步骤。

// FaultReportInput 是一次用户报障的提交内容。
type FaultReportInput struct {
	DeviceID    string  `json:"device_id"`
	UserID      uint64  `json:"-"`
	FaultType   string  `json:"fault_type"`
	Description *string `json:"description"`
	ImagesJSON  string  `json:"-"`
}

// InsertFaultReport 落一张报障单并追加首条流转事件，返回新单主键。
//
// 首条事件的 assigned_to 填提交者自己：assigned_to 是非空必填列，而用户
// 报障尚未指派运营人员，填提交者既满足约束又符合语义——用户是此刻唯一的
// 参与者。用户可见的流转因此从第一步就接得上。
func InsertFaultReport(tx *gorm.DB, in FaultReportInput) (uint64, error) {
	if err := tx.Table("device_fault_report").Create(map[string]any{
		"device_id": in.DeviceID, "user_id": in.UserID, "report_source": "user",
		"fault_type": in.FaultType, "description": in.Description,
		"images_json": in.ImagesJSON, "status": "open",
	}).Error; err != nil {
		return 0, err
	}
	id, err := persist.LastInsertID(tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Table("device_fault_report_event").Create(map[string]any{
		"report_id": id, "event_type": "reported", "from_status": "", "to_status": "open",
		"actor_id": in.UserID, "assigned_to": in.UserID, "user_visible": true, "note": "用户提交报修",
	}).Error; err != nil {
		return 0, err
	}
	return id, nil
}

// ListUserFaultReports 分页返回用户自己提交的报障单。
func ListUserFaultReports(ctx context.Context, db *gorm.DB, userID uint64, page, pageSize int) (persist.Page[map[string]any], error) {
	out := persist.Page[map[string]any]{Items: []map[string]any{}, Page: page, PageSize: pageSize}
	base := db.WithContext(ctx).Table("device_fault_report").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	if err := base.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := base.Select("id, device_id, fault_type, description, status, resolved_at, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}

// OwnsFaultReport 确认报障单属于该用户。归属判断留在查询条件里，
// 不用"先查出来再比 user_id"的做法。
func OwnsFaultReport(ctx context.Context, db *gorm.DB, id, userID uint64) (bool, error) {
	var owned int64
	err := db.WithContext(ctx).Table("device_fault_report").
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Count(&owned).Error
	return owned > 0, err
}

// ListUserVisibleFaultEvents 返回报障单中对用户可见的流转步骤。
// user_visible = 0 的内部分派信息不外泄。
func ListUserVisibleFaultEvents(ctx context.Context, db *gorm.DB, reportID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("device_fault_report_event").
		Where("report_id = ? AND user_visible = 1", reportID).
		Select("event_type, from_status, to_status, note, created_at").
		Order("id ASC").Find(&rows).Error
	return rows, err
}
