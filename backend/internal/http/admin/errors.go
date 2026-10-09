package admin

import (
	"errors"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// AdminManagementFailure 为 c 映射 err 输入、越权、身份和角色缺失，其余复用通用资源错误。
func AdminManagementFailure(c *gin.Context, err error) {
	var input *ops.ManagementInputError
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Message)
	case errors.Is(err, ops.ErrInsufficient), errors.Is(err, ops.ErrPermission):
		httpapi.Write(c, 403, 1003, err.Error(), nil)
	case errors.Is(err, operations.ErrCredentials):
		httpapi.Write(c, 401, 1001, "用户名或密码错误，或登录已失效", nil)
	case errors.Is(err, ops.ErrManagementRoleMissing):
		httpapi.Write(c, 404, 1004, err.Error(), nil)
	default:
		AdminResourceFailure(c, err)
	}
}
