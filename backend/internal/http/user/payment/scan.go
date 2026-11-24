package payment

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	gatewaycontract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/device"
	money "wattharbor/internal/domain/money"
	"wattharbor/internal/platform/httpapi"
)

var userScanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// ScanAPI 提供匿名设备或端口扫码查询。
type ScanAPI struct {
	Discovery device.DiscoveryService // 设备发现用例；匿名查询不创建支付或会话
}

// Register 注册两个匿名扫码查询接口。
func (a ScanAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/resolve", a.resolve)
	router.POST("/api/v1/user/scan/port", a.port)
}

// resolve 解析裸码或 HTTPS 二维码链接，返回设备或端口。
func (a ScanAPI) resolve(c *gin.Context) { a.handle(c, false) }

// port 按端口编码查询，只返回单个端口。
func (a ScanAPI) port(c *gin.Context) { a.handle(c, true) }

// handle 根据 requirePort 选择端口编码或扫码内容，调用发现服务。
// 此接口允许匿名访问，创建支付时才要求登录。
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

// canonicalScanCode 接受合法裸码或含唯一 code 参数的 HTTPS 链接；非法输入返回 false。
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

// Lookup 查询扫描码；查不到返回 404，读取失败返回 503。
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
