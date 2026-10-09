package device

import (
	"chargepilot/internal/device"
	authhttp "chargepilot/internal/http/auth"
	"github.com/gin-gonic/gin"
)

// API 将本业务族的用户请求交给用例，字段由 cmd 装配。
type API struct {
	Stations device.UserStationService     // 读取公开站点及设备摘要，不要求用户身份。
	Faults   device.FaultService           // 执行用户故障提交、查询和可见历史。
	Auth     authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的用户路由；只装配处理器，不执行查询或写入。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/station/nearby", a.nearbyStations)
	r.GET("/api/v1/user/station/:id", a.stationDetail)
	r.POST("/api/v1/user/device/report-fault", a.reportFault)
	r.GET("/api/v1/user/device/fault-reports", a.myFaultReports)
	r.GET("/api/v1/user/device/fault-reports/:id/history", a.myFaultHistory)
}
