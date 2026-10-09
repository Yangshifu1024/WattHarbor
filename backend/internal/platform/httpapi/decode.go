package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DecodeResource 将c的请求体限制为64KB并严格解码到dst，拒绝未知字段和多个JSON值。
// dst须是json.Decoder支持的非nil目标指针；成功返回true，调用方继续业务参数校验。
// 格式、大小或目标错误写入HTTP 400 响应并返回false，调用方必须立即返回。
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
