package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DecodeResource 将请求体限制为 64 KiB，解码到非 nil 指针 dst，拒绝未知字段和多个 JSON 值。
// 失败时写入 HTTP 400 并返回 false，调用方必须立即返回；成功后仍需校验业务参数。
func DecodeResource(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		BadRequest(c, "请求内容无效或包含未知字段")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		BadRequest(c, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
