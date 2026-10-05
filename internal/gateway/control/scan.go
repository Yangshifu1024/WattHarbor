package control

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

var scanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

type ScanAPI struct {
	Store interface {
		ResolveScan(context.Context, string) (store.ScanResult, error)
	}
	ServiceToken string
}

func (a ScanAPI) Register(router *gin.Engine) {
	router.GET(contract.ScanResolvePath, a.resolve)
}

func (a ScanAPI) resolve(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	code := c.Query("code")
	if !scanCodePattern.MatchString(code) {
		httpapi.BadRequest(c, "invalid scan code")
		return
	}
	result, err := a.Store.ResolveScan(c.Request.Context(), code)
	if errors.Is(err, store.ErrScanNotFound) {
		httpapi.Write(c, http.StatusNotFound, 1004, "scan code not found", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "device lookup unavailable", nil)
		return
	}
	out := contract.ScanReply{Kind: result.Kind, DeviceID: result.DeviceID, StationID: result.StationID}
	if result.Port != nil {
		p := scanPortReply(*result.Port)
		out.Port = &p
	}
	if result.Ports != nil {
		out.Ports = make([]contract.ScanPort, len(result.Ports))
		for i, p := range result.Ports {
			out.Ports[i] = scanPortReply(p)
		}
	}
	httpapi.OK(c, out)
}

// scanPortReply 将row所属端口读模型映射为共享子记录，保留原单位与身份，无IO。
func scanPortReply(row store.ScanPort) contract.ScanPort {
	return contract.ScanPort{PortID: row.PortID, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: row.Status, Online: row.Online, Available: row.Available}
}
