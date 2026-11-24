package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// mfaSettings 校验 MFA 设置输入，以当前管理员和会话执行设置操作。
func (a AdminAuth) mfaSettings(c *gin.Context) {
	var input ops.MFASettingsInput
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	input.Code = strings.TrimSpace(input.Code)
	if !input.Valid() {
		httpapi.BadRequest(c, ops.ErrMFAInput.Error())
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	reply, err := a.MFA.Apply(c.Request.Context(), p, c.GetString("admin_sid"), input, c.ClientIP())
	if err != nil {
		a.mfaSettingsFailure(c, err)
		return
	}
	httpapi.OK(c, reply)
}

// mfaSettingsFailure 将无效验证码转换为 400、设置状态冲突转换为 409、频率限制转换为 429。
func (a AdminAuth) mfaSettingsFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ops.ErrMFAInput):
		httpapi.BadRequest(c, err.Error())
	case errors.Is(err, operations.ErrUsedTOTP):
		httpapi.Write(c, http.StatusBadRequest, 1002, operations.ErrUsedTOTP.Error(), nil)
	case errors.Is(err, ops.ErrMFAProof), errors.Is(err, operations.ErrInvalidTOTP):
		httpapi.Write(c, http.StatusBadRequest, 1002, ops.ErrMFAProof.Error(), nil)
	case errors.Is(err, ops.ErrMFAPending), errors.Is(err, ops.ErrMFAState), errors.Is(err, ops.ErrMFABusy):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	case errors.Is(err, ops.ErrMFASettingsRate):
		c.Header("Retry-After", "60")
		httpapi.Write(c, 429, 4291, err.Error(), nil)
	default:
		a.failure(c, err)
	}
}
