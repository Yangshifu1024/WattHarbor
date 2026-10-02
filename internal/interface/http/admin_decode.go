package http

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DecodeResource 限制请求体为 64 KB，拒绝未知字段及多个 JSON 对象。
// 后台写入类接口在解析成功后继续执行各自的参数校验。
func DecodeResource(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		httpapi.BadRequest(c, "请求内容无效或包含未知字段")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		httpapi.BadRequest(c, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
