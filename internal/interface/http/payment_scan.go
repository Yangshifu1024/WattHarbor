package http

import (
	"context"
	"errors"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

var userScanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

type ScanAPI struct {
	Discovery device.DiscoveryService // 设备发现用例；匿名查询不创建支付或会话
}

func (a ScanAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/resolve", a.resolve)
	router.POST("/api/v1/user/scan/port", a.port)
}

func (a ScanAPI) resolve(c *gin.Context) { a.handle(c, false) }
func (a ScanAPI) port(c *gin.Context)    { a.handle(c, true) }

func (a ScanAPI) handle(c *gin.Context, requirePort bool) {
	// 设备和端口的发现接口是公开的。
	// 只有客户在 /scan/start 创建支付意图时才需要鉴权。
	var body struct {
		Code   string `json:"code"`
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

// App 发来的是未经加工的二维码载荷。
// 客户二维码链接里可能在 `code` 中带印在纸上的设备/端口码，裸码也同样有效。
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

// Lookup 将设备发现错误映射为原 HTTP 适配状态；业务用例直接调用 Discovery。
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
