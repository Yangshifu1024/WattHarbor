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

// registerImports 注册设备导入列表、创建和重试接口，均要求 device.import 权限。
func (a API) registerImports(r *gin.Engine) {
	r.GET("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.imports)
	r.POST("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.createImport)
	r.POST("/api/v1/admin/device-imports/:import_id/retry", a.Auth.Require("device.import"), a.retryImport)
}

// imports 返回最近一百个设备导入任务。
func (a API) imports(c *gin.Context) {
	rows, err := a.Imports.List(c.Request.Context())
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, rows)
}

// createImport 校验导入请求和管理员当前范围，创建任务并同步执行。
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

// retryImport 按任务中保存的设备清单重试导入。
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

// importAudit 取得已鉴权的管理员、客户端 IP 和请求编号。
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

// importFailure 将导入参数、范围和厂商错误转换为接口响应。
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
