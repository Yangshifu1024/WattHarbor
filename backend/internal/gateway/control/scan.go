package control

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

var scanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// ScanAPI 适配内部扫描码读取，仅依赖 ResolveScan 窄能力并输出共享扫描响应，不创建业务命令。
type ScanAPI struct {
	// Scanner 仅解析扫描码的窄能力，不创建充电命令。
	Scanner interface {
		// ResolveScan 接收请求 context 与原扫描码，返回扫描设备或端口读模型及缺失、读取错误；不创建充电或支付命令。
		ResolveScan(context.Context, string) (repo.ScanResult, error)
	}
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 接收非 nil router，注册本类型原路由与处理器，无数据库或设备 IO，不启动 HTTP 监听。
func (a ScanAPI) Register(router *gin.Engine) {
	router.GET(contract.ScanResolvePath, a.resolve)
}

// resolve 接收 c，鉴权并校验扫描码后通过 Scanner 读取事实，转换共享响应；写原 200、400、401、404 或 503，无命令写入。
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

// scanPortReply 将row所属端口读模型映射为共享子记录，保留原单位与身份，无IO。
func scanPortReply(row repo.ScanPort) contract.ScanPort {
	return contract.ScanPort{PortID: row.PortID, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: row.Status, Online: row.Online, Available: row.Available}
}
