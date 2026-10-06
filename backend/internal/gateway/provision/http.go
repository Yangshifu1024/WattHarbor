package provision

import (
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"unicode/utf8"
)

// API 适配内部设备开通和厂商管理接口；开通由仓储执行原整批事务，HTTP保留服务鉴权与字段校验。
type API struct {
	// DB 装配方提供的 gateway GORM 连接句柄；本适配器不关闭连接池，开通批次的事务由仓储管理。
	DB *gorm.DB
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a API) Register(r *gin.Engine) {
	r.POST(contract.ProvisionPath, a.provision)
	a.registerVendors(r)
}

// provision 接收 c，鉴权并校验有界开通请求，将五项原身份字段按输入顺序交仓储；成功写原 provisioned 数量（含重放）。
// 校验、冲突或 SQL／取消失败写原 400、401、409、503；不发送设备命令，不确认物理设备上线。
func (a API) provision(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var in contract.ProvisionRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if c.ShouldBindJSON(&in) != nil || len(in.Devices) == 0 || len(in.Devices) > 100 {
		httpapi.BadRequest(c, "invalid device batch")
		return
	}
	seen := map[string]bool{}
	for _, d := range in.Devices {
		k := strings.ToLower(d.DeviceID)
		if !device.ValidDeviceID(d.DeviceID) || d.StationID == 0 || d.VendorID == 0 || d.PortCount == 0 || seen[k] || (d.Model != nil && utf8.RuneCountInString(*d.Model) > 128) {
			httpapi.BadRequest(c, "invalid device parameters")
			return
		}
		seen[k] = true
	}
	devices := make([]gatewayrepo.ProvisionDevice, len(in.Devices))
	for i, d := range in.Devices {
		devices[i] = gatewayrepo.ProvisionDevice{
			DeviceID: d.DeviceID, VendorID: d.VendorID, StationID: d.StationID, PortCount: d.PortCount, Model: d.Model,
		}
	}
	err := gatewayrepo.ProvisionBatch(c.Request.Context(), a.DB, devices)
	if err != nil {
		if errors.Is(err, gatewayrepo.ErrProvisionConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, "设备已存在且参数不同，或厂商无效（dc589 最多 20 路）", nil)
		} else {
			httpapi.Write(c, 503, 5003, "设备配置暂时不可用", nil)
		}
		return
	}
	httpapi.OK(c, contract.ProvisionReply{Provisioned: len(in.Devices)})
}
