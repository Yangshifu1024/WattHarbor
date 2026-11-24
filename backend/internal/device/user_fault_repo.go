package device

import (
	"context"
	"wattharbor/internal/persist"

	"gorm.io/gorm"
)

// 用户只能提交和查看本人报障，以及处理过程中标为用户可见的步骤；后台表访问在 fault_repo.go。

// FaultReportInput 是一次用户报障的提交内容。
type FaultReportInput struct {
	DeviceID string `json:"device_id"` // 用户报告设备编号
	UserID   uint64 `json:"-"`         // 提交报障的用户 ID，不在该输入对象的 JSON 中输出
	// device_fault_report.fault_type 五值；调用方已完成词表校验。
	FaultType   FaultType `json:"fault_type"` // 故障类型
	Description *string   `json:"description"`
	ImagesJSON  string    `json:"-"` // 图片数组 JSON；未提交图片时为文本 null
}

// InsertFaultReport 保存报障和首条事件，返回新主键。
// 首条事件 assigned_to 保留提交用户，报告本身仍未指派，不据此认定用户有处理权限。
func InsertFaultReport(tx *gorm.DB, in FaultReportInput) (uint64, error) {
	if err := tx.Table("device_fault_report").Create(map[string]any{
		// report_source 固定 user：inspect／monitor 当前无写入路径（见 ReportSource）。
		"device_id": in.DeviceID, "user_id": in.UserID, "report_source": ReportSourceUser,
		"fault_type": in.FaultType, "description": in.Description,
		"images_json": in.ImagesJSON, "status": FaultOpen,
	}).Error; err != nil {
		return 0, err
	}
	id, err := persist.LastInsertID(tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Table("device_fault_report_event").Create(map[string]any{
		// 用户报修入口写入 reported（不在 BackgroundWritable 四值内）；from_status 写空串
		// 而非 null，保持现有列写法不变。
		"report_id": id, "event_type": FaultEventReported, "from_status": FaultStatus(""), "to_status": FaultOpen,
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

// OwnsFaultReport 在查询条件中检查报告用户，避免读出他人记录后才比较。
func OwnsFaultReport(ctx context.Context, db *gorm.DB, id, userID uint64) (bool, error) {
	var owned int64
	err := db.WithContext(ctx).Table("device_fault_report").
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Count(&owned).Error
	return owned > 0, err
}

// ListUserVisibleFaultEvents 只读取用户可见步骤，不返回 user_visible=0 的内部事件。
func ListUserVisibleFaultEvents(ctx context.Context, db *gorm.DB, reportID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("device_fault_report_event").
		Where("report_id = ? AND user_visible = 1", reportID).
		Select("event_type, from_status, to_status, note, created_at").
		Order("id ASC").Find(&rows).Error
	return rows, err
}
