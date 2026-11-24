package control

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
	contract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/gateway/repo"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

var scanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// ScanAPI 提供内部扫描码查询，返回设备或端口信息。
type ScanAPI struct {
	// Scanner 解析扫描码并查询设备或端口。
	Scanner interface {
		// ResolveScan 查询扫描码；未匹配到设备或端口时返回错误。
		ResolveScan(context.Context, string) (repo.ScanResult, error)
	}
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 注册内部扫码查询路由。
func (a ScanAPI) Register(router *gin.Engine) {
	router.GET(contract.ScanResolvePath, a.resolve)
}

// resolve 校验服务令牌和扫描码，返回设备或端口；查不到返回 404。
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
	result, err := a.Scanner.ResolveScan(c.Request.Context(), code)
	if errors.Is(err, repo.ErrScanNotFound) {
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

// scanPortReply 将端口查询结果转换为共享响应。
func scanPortReply(row repo.ScanPort) contract.ScanPort {
	return contract.ScanPort{PortID: row.PortID, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: row.Status, Online: row.Online, Available: row.Available}
}
