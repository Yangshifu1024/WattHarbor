// Package user 提供用户接口共用的状态校验和错误响应。
package user

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"wattharbor/internal/platform/httpapi"
)

// WriteResourceFailure 将 err 写入 c 的错误响应：缺失记录为 404／1004，其他错误为 503／5003；不返回错误或读取、修改业务资源。
func WriteResourceFailure(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "记录不存在", nil)
		return
	}
	httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
}
