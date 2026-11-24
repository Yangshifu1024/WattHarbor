// Package httpapi 提供统一的 HTTP 响应、参数校验、监控和服务停机处理。
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const requestIDKey = "wattharbor.request_id"

// Envelope 是统一响应格式；RequestID 与 TraceID 使用同一请求编号。
type Envelope struct {
	Code      int        `json:"code"`
	Message   string     `json:"message"`
	Data      any        `json:"data"`
	RequestID string     `json:"request_id"`
	TraceID   string     `json:"trace_id"`
	Errors    []FieldErr `json:"errors,omitempty"`
}

// FieldErr 指明参数名及其未满足的校验规则。
type FieldErr struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

// NewRouter 安装异常恢复和请求编号中间件，可选安装请求指标。
// 未匹配的路径和方法分别返回统一的 404、405 响应；不信任代理转发的客户端 IP。
func NewRouter(metrics ...*Metrics) *gin.Engine {
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.Recovery(), Trace())
	// 用路由模板统计请求，避免每个设备或订单编号各生成一组指标。
	for _, m := range metrics {
		if m != nil {
			router.Use(m.Middleware())
		}
	}
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) {
		Write(c, http.StatusNotFound, CodeNotFound, "接口尚未提供，请确认服务版本", nil)
	})
	router.NoMethod(func(c *gin.Context) {
		Write(c, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "请求方法不支持", nil)
	})
	return router
}

// Trace 沿用有效的 X-Request-ID，否则生成随机编号，并写入响应头。
func Trace() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if len(id) == 0 || len(id) > 128 || !validID(id) {
			id = randomID()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// RequestID 读取 Trace 为当前请求保存的编号。
func RequestID(c *gin.Context) string { return c.GetString(requestIDKey) }

// Write 发送统一 JSON 响应并禁止缓存；没有字段错误时省略 errors。
func Write(c *gin.Context, status, code int, message string, data any, fields ...FieldErr) {
	c.Header("Cache-Control", "no-store")
	id := RequestID(c)
	c.JSON(status, Envelope{Code: code, Message: message, Data: data, RequestID: id, TraceID: id, Errors: fields})
}

// OK 发送 HTTP 200 和业务码 0 的响应。
func OK(c *gin.Context, data any) { Write(c, http.StatusOK, 0, "ok", data) }

// BadRequest 发送 HTTP 400 和参数无效的业务码，可附字段校验错误。
func BadRequest(c *gin.Context, message string, fields ...FieldErr) {
	Write(c, http.StatusBadRequest, CodeBadRequest, message, nil, fields...)
}

func validID(id string) bool {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// DefaultPageLimit 是 C 端页码上限；后台使用更大的 MaxAdminPageLimit，便于查询历史记录。
const (
	DefaultPageLimit  = 100000
	MaxAdminPageLimit = 1000000
)

// MaxPageSize 是各接口共用的每页条数上限。
const MaxPageSize = 100

// ReadPaging 解析 page / page_size 查询参数，默认每页 fallback 条。
// 参数无效时写 400 响应并返回 ok=false，调用方应直接返回。
// maxPage 指定页码上限，非正数时使用 DefaultPageLimit。
func ReadPaging(c *gin.Context, fallback, maxPage int) (page, size int, ok bool) {
	if maxPage <= 0 {
		maxPage = DefaultPageLimit
	}
	page, size = 1, fallback
	if raw := c.Query("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPage {
			BadRequest(c, "分页参数无效")
			return 0, 0, false
		}
		page = parsed
	}
	if raw := c.Query("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > MaxPageSize {
			BadRequest(c, "分页参数无效：page_size 为 1–100")
			return 0, 0, false
		}
		size = parsed
	}
	return page, size, true
}
