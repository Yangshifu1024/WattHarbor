package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// mfaSettings 从c严格提取输入，操作人仅取已鉴权原档案与SID，返回所属安全设置回执。
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

// mfaSettingsFailure 为c映射err原证明400、冲突409或频次429，其余复用认证错误。
func (a AdminAuth) mfaSettingsFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ops.ErrMFAInput):
		httpapi.BadRequest(c, err.Error())
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
