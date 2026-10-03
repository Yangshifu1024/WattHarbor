package http

import (
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
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
		created, err := device.InsertFaultReport(tx, device.FaultReportInput{
			DeviceID: in.DeviceID, UserID: userID, FaultType: in.FaultType,
			Description: in.Description, ImagesJSON: string(images),
		})
		if err != nil {
			return err
		}
		id = created
		return nil
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
	out, err := device.ListUserFaultReports(c.Request.Context(), a.UserDB, userID, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": out.Items, "total": out.Total, "page": page, "page_size": pageSize})
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
	owned, err := device.OwnsFaultReport(c.Request.Context(), a.UserDB, id, userID)
	if err != nil || !owned {
		// 别人的报障单与不存在的报障单返回完全一样。
		httpapi.Write(c, 404, 1004, "报修记录不存在", nil)
		return
	}
	rows, err := device.ListUserVisibleFaultEvents(c.Request.Context(), a.UserDB, id)
	if err != nil {
		httpapi.Write(c, 503, 5003, "处理进度暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// ---- 发票 ----
