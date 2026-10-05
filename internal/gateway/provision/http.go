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

// Device 是一台待开通设备的参数，定义在持久化层。
type Device = gatewayrepo.ProvisionDevice

// API 适配内部设备开通和厂商管理接口，保留原服务鉴权、批次事务与字段校验。
type API struct {
	// DB 装配方提供的 gateway GORM 连接句柄；此适配器不关闭连接池，具体事务由原方法管理。
	DB *gorm.DB
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a API) Register(r *gin.Engine) {
	r.POST(contract.ProvisionPath, a.provision)
	a.registerVendors(r)
}

// provision 接收 c，鉴权并校验有界设备批次，在同一事务依次开通设备；成功写原 provisioned 数量，校验、冲突或 SQL 失败写原 400、401、409、503。
func (a API) provision(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var in struct {
		// Devices 为本次待开通设备集合，保留原批次校验与事务边界。
		Devices []Device `json:"devices"`
	}
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
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, d := range in.Devices {
			if err := gatewayrepo.ProvisionOne(tx, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gatewayrepo.ErrProvisionConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, "设备已存在且参数不同，或厂商无效（dc589 最多 20 路）", nil)
		} else {
			httpapi.Write(c, 503, 5003, "设备配置暂时不可用", nil)
		}
		return
	}
	httpapi.OK(c, gin.H{"provisioned": len(in.Devices)})
}
