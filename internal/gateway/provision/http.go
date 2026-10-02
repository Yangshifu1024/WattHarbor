package provision

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Device 是一台待开通设备的参数，定义在持久化层。
type Device = gatewaystore.ProvisionDevice
type API struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a API) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/devices/provision", a.provision)
	a.registerVendors(r)
}

func (a API) provision(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var in struct {
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
		if !devices.ValidDeviceID(d.DeviceID) || d.StationID == 0 || d.VendorID == 0 || d.PortCount == 0 || seen[k] || (d.Model != nil && utf8.RuneCountInString(*d.Model) > 128) {
			httpapi.BadRequest(c, "invalid device parameters")
			return
		}
		seen[k] = true
	}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, d := range in.Devices {
			if err := gatewaystore.ProvisionOne(tx, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gatewaystore.ErrProvisionConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, "设备已存在且参数不同，或厂商无效（dc589 最多 20 路）", nil)
		} else {
			httpapi.Write(c, 503, 5003, "设备配置暂时不可用", nil)
		}
		return
	}
	httpapi.OK(c, gin.H{"provisioned": len(in.Devices)})
}
