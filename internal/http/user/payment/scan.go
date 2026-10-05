package payment

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

var userScanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// ScanAPI 提供匿名二维码和端口读模型，不创建用户会话、支付或设备命令。
type ScanAPI struct {
	Discovery device.DiscoveryService // 设备发现用例；匿名查询不创建支付或会话
}

// Register 向 router 注册两个匿名扫码查询端点，无返回值或业务 IO。
func (a ScanAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/resolve", a.resolve)
	router.POST("/api/v1/user/scan/port", a.port)
}

// resolve 接收 c 的扫码请求，委托 handle 解析裸码或 HTTPS 二维码链接并写设备或端口读模型；不创建会话或支付。
func (a ScanAPI) resolve(c *gin.Context) { a.handle(c, false) }

// port 接收 c 的端口编码请求，委托 handle 仅输出端口读模型；输入、缺失及读取错误保持 400／404／503。
func (a ScanAPI) port(c *gin.Context) { a.handle(c, true) }

// handle 从 c 绑定扫码输入，requirePort 决定读取 port_id 或解析 code，并在请求 context 下查询设备发现用例。
// 成功写扫码结果或端口；参数错误为 400、缺失或非端口结果为 404、依赖失败为 503，无会话或支付写入。
func (a ScanAPI) handle(c *gin.Context, requirePort bool) {
	// 设备和端口的发现接口是公开的。
	// 只有客户在 /scan/start 创建支付意图时才需要鉴权。
	var body struct {
		// Code 扫码解析输入，可为裸码或含唯一 code 的 HTTPS 链接。
		Code string `json:"code"`
		// PortID 端口查询输入，仅接受合法裸码。
		PortID string `json:"port_id"`
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid scan request")
		return
	}
	code := body.Code
	if requirePort {
		code = body.PortID
	}
	if !requirePort {
		var valid bool
		code, valid = canonicalScanCode(code)
		if !valid {
			httpapi.BadRequest(c, "invalid scan code")
			return
		}
	} else if !userScanCodePattern.MatchString(code) {
		httpapi.BadRequest(c, "invalid scan code")
		return
	}
	result, status := a.Lookup(c.Request.Context(), code)
	if status != http.StatusOK {
		switch status {
		case http.StatusNotFound:
			httpapi.Write(c, http.StatusNotFound, 1004, "scan code not found", nil)
		default:
			httpapi.Write(c, http.StatusServiceUnavailable, 5001, "device lookup unavailable", nil)
		}
		return
	}
	if requirePort {
		if result.Kind != "port" || result.Port == nil {
			httpapi.Write(c, http.StatusNotFound, 1004, "port not found", nil)
			return
		}
		httpapi.OK(c, result.Port)
		return
	}
	httpapi.OK(c, result)
}

// canonicalScanCode 接收 raw 二维码载荷，返回裸码或 HTTPS 链接中唯一合法 code 及 true；超长、非法文本或链接返回空串及 false，无 IO。
func canonicalScanCode(raw string) (string, bool) {
	if userScanCodePattern.MatchString(raw) {
		return raw, true
	}
	if len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n\t") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", false
	}
	values := parsed.Query()["code"]
	if len(values) != 1 || !userScanCodePattern.MatchString(values[0]) {
		return "", false
	}
	return values[0], true
}

// Lookup 在 ctx 下用 code 调用设备发现用例，返回读模型和 HTTP 状态；缺失映射 404、其余错误映射 503，成功为 200，无业务写入。
func (a ScanAPI) Lookup(ctx context.Context, code string) (money.ScanResult, int) {
	out, err := a.Discovery.ResolveScan(ctx, code)
	if errors.Is(err, gatewaycontract.ErrScanNotFound) {
		return out, http.StatusNotFound
	}
	if err != nil {
		return out, http.StatusServiceUnavailable
	}
	return out, http.StatusOK
}
