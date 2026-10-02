package http

import (
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
)

// 设备报障的提交与查询。
func (a UserAccountAPI) reportFault(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		DeviceID    string   `json:"device_id"`
		PortNo      uint8    `json:"port_no"`
		FaultType   string   `json:"fault_type"`
		Description *string  `json:"description"`
		Images      []string `json:"images"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "报修请求无效")
		return
	}
	if in.DeviceID == "" || len(in.DeviceID) > 64 || !oneOfStatus(in.FaultType, "mechanical electrical communication display other") {
		httpapi.BadRequest(c, "设备或故障类型无效")
		return
	}
	if in.Description != nil && len([]rune(*in.Description)) > 2000 {
		httpapi.BadRequest(c, "故障描述过长")
		return
	}
	if len(in.Images) > 9 {
		httpapi.BadRequest(c, "图片最多 9 张")
		return
	}
	images, _ := json.Marshal(in.Images)
	var id uint64
	err := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("device_fault_report").Create(map[string]any{
			"device_id": in.DeviceID, "user_id": userID, "report_source": "user",
			"fault_type": in.FaultType, "description": in.Description,
			"images_json": string(images), "status": "open",
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		// 新报障尚未指派运营人员，assigned_to 为必填字段，初始记录提交用户。
		return tx.Table("device_fault_report_event").Create(map[string]any{
			"report_id": id, "event_type": "reported", "from_status": "", "to_status": "open",
			"actor_id": userID, "assigned_to": userID, "user_visible": true, "note": "用户提交报修",
		}).Error
	})
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "open"})
}

func (a UserAccountAPI) myFaultReports(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	base := a.UserDB.WithContext(c.Request.Context()).Table("device_fault_report").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	rows := []map[string]any{}
	if err := base.Select("id, device_id, fault_type, description, status, resolved_at, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (a UserAccountAPI) myFaultHistory(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "报修编号无效")
		return
	}
	var owned int64
	if err := a.UserDB.WithContext(c.Request.Context()).Table("device_fault_report").
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Count(&owned).Error; err != nil || owned == 0 {
		// 别人的报障单与不存在的报障单返回完全一样。
		httpapi.Write(c, 404, 1004, "报修记录不存在", nil)
		return
	}
	rows := []map[string]any{}
	// 只暴露客户可见的流转步骤；内部分派信息不外泄。
	if err := a.UserDB.WithContext(c.Request.Context()).Table("device_fault_report_event").
		Where("report_id = ? AND user_visible = 1", id).
		Select("event_type, from_status, to_status, note, created_at").
		Order("id ASC").Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "处理进度暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// ---- 发票 ----
