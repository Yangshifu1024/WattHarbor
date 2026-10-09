package device

import (
	"errors"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/device"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/pricing"
	"github.com/gin-gonic/gin"
)

// registerImports 将 r 绑定导入列表、创建及重试路径，三者继续使用 device.import 权限。
func (a API) registerImports(r *gin.Engine) {
	r.GET("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.imports)
	r.POST("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.createImport)
	r.POST("/api/v1/admin/device-imports/:import_id/retry", a.Auth.Require("device.import"), a.retryImport)
}

// imports 在请求内返回最近一百个任务或读取失败，不输出存档清单，也不执行任务。
func (a API) imports(c *gin.Context) {
	rows, err := a.Imports.List(c.Request.Context())
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, rows)
}

// createImport 解码批量请求，取得当前范围并调用完整受理／同步执行用例，返回任务或一次错误响应。
func (a API) createImport(c *gin.Context) {
	var in device.ImportInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if err := device.ValidateImportInput(in); err != nil {
		importFailure(c, err)
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Imports.Create(c.Request.Context(), importAudit(c), scope, in)
	if err != nil {
		importFailure(c, err)
		return
	}
	importResponse(c, out)
}

// retryImport 在请求内校验任务号及实时范围，以任务清单同步恢复；不重新解析当前厂商配置。
func (a API) retryImport(c *gin.Context) {
	id := c.Param("import_id")
	if !admin.RequestIDPattern.MatchString(id) {
		httpapi.BadRequest(c, "导入编号无效")
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Imports.Retry(c.Request.Context(), importAudit(c), scope, id)
	if err != nil {
		importFailure(c, err)
		return
	}
	importResponse(c, out)
}

// importAudit 从 c 取得可信当前身份、请求号及地址，不把 gin 注入业务事务。
func importAudit(c *gin.Context) ops.AuditContext {
	return ops.AuditContext{Actor: c.MustGet("admin_profile").(operations.Profile), ClientIP: c.ClientIP(), RequestID: httpapi.RequestID(c)}
}

// importResponse 将 out 执行状态映射为公开结果；failed 是已提交可重试任务，仍返回 503 及任务载荷。
func importResponse(c *gin.Context, out device.ImportJob) {
	if out.Status == "failed" {
		httpapi.Write(c, 503, 5003, *out.LastError, out)
		return
	}
	httpapi.OK(c, out)
}

// importFailure 保留输入、范围、厂商及兼容的公开码；其他错误交给统一适配，绝不在事务里写响应。
func importFailure(c *gin.Context, err error) {
	var input device.ImportInputError
	var vendor *contract.VendorFailure
	var blocked *pricing.ErrMeteringBlocked
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Error())
	case errors.Is(err, operations.ErrForbidden):
		httpapi.Write(c, 403, 1003, err.Error(), nil)
	case errors.As(err, &vendor):
		httpapi.Write(c, vendor.Status, vendor.Code, vendor.Message, nil)
	case errors.As(err, &blocked):
		httpapi.Write(c, 409, 1009, err.Error()+"，请检查所选通信协议或站点计费方式", nil)
	case errors.Is(err, device.ErrImportConflict):
		admin.AdminResourceFailure(c, admin.ErrConflict)
	default:
		admin.AdminResourceFailure(c, err)
	}
}
