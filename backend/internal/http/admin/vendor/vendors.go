package vendor

import (
	"errors"
	"net/http"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/device"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerVendors 在 r 注册厂商公开路由及实时权限中间件；无返回值、数据库操作或后台任务。
func (a API) registerVendors(r *gin.Engine) {
	r.GET("/api/v1/admin/vendors", a.Auth.Require("vendor.read"), a.listVendors)
	r.GET("/api/v1/admin/vendors/:id", a.Auth.Require("vendor.read"), a.getVendor)
	r.POST("/api/v1/admin/vendors", a.Auth.Require("vendor.create"), a.createVendor)
	r.PUT("/api/v1/admin/vendors/:id", a.Auth.Require("vendor.update"), a.updateVendor)
	r.GET("/api/v1/admin/vendor-options", a.Auth.Require("device.import"), a.vendorOptions)
}

// listVendors 从 c 解析分页、读取实时范围并返回用例结果，公开列表允许启用或禁用筛选。
func (a API) listVendors(c *gin.Context) { a.listVendorPage(c, false) }

// vendorOptions 从 c 返回当前范围内的启用厂商选项，不额外要求厂商管理权限。
func (a API) vendorOptions(c *gin.Context) { a.listVendorPage(c, true) }

// listVendorPage 从 c 解析分页并调用用例，options 选择导入选项；返回原分页及本地权限或安全错误。
func (a API) listVendorPage(c *gin.Context, options bool) {
	page, ok := admin.ReadAdminPage(c, "enabled disabled")
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Vendors.List(c.Request.Context(), admin.AdminAuditContext(c), scope, page, options)
	if err != nil {
		VendorFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// getVendor 从 c 解析主键及实时范围，调用用例读取公开字典；返回原资料或范围／服务错误。
func (a API) getVendor(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Vendors.Get(c.Request.Context(), admin.AdminAuditContext(c), scope, id)
	if err != nil {
		VendorFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// createVendor 从 c 严格解析厂商完整字段，以实时范围和权限调用新建用例；返回网关确认结果或原错误。
func (a API) createVendor(c *gin.Context) { a.saveVendor(c, 0) }

// updateVendor 从 c 解析主键，以实时范围及完整字段调用更新用例；返回网关确认结果或原错误。
func (a API) updateVendor(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	a.saveVendor(c, id)
}

// saveVendor 读取 c 的实时范围及原输入，以 id 选择新建或更新；用例负责外部调用与提交后审计。
func (a API) saveVendor(c *gin.Context, id uint64) {
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	// 保留原受限创建先拒绝范围、再解析输入的响应顺序。
	if id == 0 && !scope.Unrestricted && len(scope.VendorIDs) > 0 {
		httpapi.Write(c, 403, 1003, "创建厂商需要不受厂商数据范围限制的账号", nil)
		return
	}
	if id != 0 && !scope.AllowsVendor(id) {
		VendorFailure(c, device.VendorNotVisible{})
		return
	}
	var in contract.VendorRequest
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	out, err := a.Vendors.Save(c.Request.Context(), admin.AdminAuditContext(c), scope, id, in)
	if err != nil {
		VendorFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// VendorFailure 把 err 的原公开业务码、范围缺失或权限写入 c；其他异常统一依赖不可用，不透传认证错误。
func VendorFailure(c *gin.Context, err error) {
	var service *contract.VendorFailure
	var invisible device.VendorNotVisible
	switch {
	case errors.As(err, &service):
		httpapi.Write(c, service.Status, service.Code, service.Message, nil)
	case errors.As(err, &invisible):
		httpapi.Write(c, http.StatusNotFound, 1004, invisible.Error(), nil)
	case errors.Is(err, operations.ErrForbidden):
		httpapi.Write(c, 403, 1003, "创建厂商需要不受厂商数据范围限制的账号", nil)
	default:
		httpapi.Write(c, 503, 5003, "厂商服务暂时不可用，请稍后重试", nil)
	}
}
