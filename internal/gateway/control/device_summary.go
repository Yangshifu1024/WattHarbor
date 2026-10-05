package control

import (
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DeviceSummaryAPI 将所属网关 repo 的事实映射为共享摘要 DTO，只有服务令牌可读取。
type DeviceSummaryAPI struct {
	DB           *gorm.DB // gateway 运行事实连接
	ServiceToken string   // 由进程注入的服务鉴权令牌
}

// Register 在 r 注册批量摘要端点；无返回值，不启动任务或读取数据库。
func (a DeviceSummaryAPI) Register(r *gin.Engine) {
	r.GET(contract.DeviceSummariesPath, a.list)
}

// list 从 c 校验令牌及最多一百台设备，读取所属两表并返回共享 DTO；失败写原 HTTP 错误。
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
	out := contract.DeviceSummariesReply{Items: make([]contract.DeviceSummary, 0, len(rows))}
	byDevice := make(map[string]int, len(rows))
	for i := range rows {
		row := rows[i]
		out.Items = append(out.Items, contract.DeviceSummary{DeviceID: row.DeviceID, VendorName: row.VendorName, LastHeartbeatAt: row.LastHeartbeatAt, SignalStrength: row.SignalStrength, SignalAt: row.SignalAt, Ports: []contract.DevicePortSummary{}})
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
				out.Items[i].Ports = append(out.Items[i].Ports, contract.DevicePortSummary{PortNo: port.PortNo, StatusCode: port.StatusCode, StatusAt: port.StatusAt})
			}
		}
	}
	httpapi.OK(c, out)
}
