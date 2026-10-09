package control

import (
	contract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"

	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DeviceSummaryAPI 返回网关设备和端口摘要，要求服务令牌。
type DeviceSummaryAPI struct {
	// DB 装配方提供的 gateway GORM 连接句柄；此适配器不关闭连接池，具体事务由方法管理。
	DB           *gorm.DB // 网关数据库连接
	ServiceToken string   // 由进程注入的服务鉴权令牌
}

// Register 在 r 注册批量摘要端点；不启动任务或读取数据库。
func (a DeviceSummaryAPI) Register(r *gin.Engine) {
	r.GET(contract.DeviceSummariesPath, a.list)
}

// list 从 c 校验令牌及最多一百台设备，读取两表并返回共享 DTO；失败写 HTTP 错误。
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
	rows, err := gatewayrepo.ListDeviceSummaries(c.Request.Context(), a.DB, ids)
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
		ports, err := gatewayrepo.ListDevicePortStatuses(c.Request.Context(), a.DB, ids)
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
