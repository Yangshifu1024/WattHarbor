package control

import (
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DevicePortSummary = gatewaystore.DevicePortStatusRow

type DeviceSummary = gatewaystore.DeviceSummaryRow

type DeviceSummaryAPI struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a DeviceSummaryAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/internal/device-summaries", a.list)
}

func (a DeviceSummaryAPI) list(c *gin.Context) {
	if !(SessionAPI{ServiceToken: a.ServiceToken}).authorized(c) {
		return
	}
	ids := c.QueryArray("device_id")
	if len(ids) == 0 || len(ids) > 100 {
		httpapi.BadRequest(c, "请选择1至100台设备")
		return
	}
	for _, id := range ids {
		if len(id) == 0 || len(id) > 64 {
			httpapi.BadRequest(c, "设备编号无效")
			return
		}
	}
	rows, err := gatewaystore.ListDeviceSummaries(c.Request.Context(), a.DB, ids)
	if err != nil {
		httpapi.Write(c, 503, 5001, "设备信息暂不可读取", nil)
		return
	}
	byDevice := make(map[string]int, len(rows))
	for i := range rows {
		rows[i].Ports = []DevicePortSummary{}
		byDevice[rows[i].DeviceID] = i
	}
	if len(rows) > 0 {
		ports, err := gatewaystore.ListDevicePortStatuses(c.Request.Context(), a.DB, ids)
		if err != nil {
			httpapi.Write(c, 503, 5001, "设备端口状态暂不可读取", nil)
			return
		}
		for _, port := range ports {
			if i, ok := byDevice[port.DeviceID]; ok {
				rows[i].Ports = append(rows[i].Ports, port)
			}
		}
	}
	httpapi.OK(c, gin.H{"items": rows})
}
