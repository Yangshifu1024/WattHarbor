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

// registerVendors 注册厂商查询和管理接口，并校验各自权限。
func (a API) registerVendors(r *gin.Engine) {
	r.GET("/api/v1/admin/vendors", a.Auth.Require("vendor.read"), a.listVendors)
	r.GET("/api/v1/admin/vendors/:id", a.Auth.Require("vendor.read"), a.getVendor)
	r.POST("/api/v1/admin/vendors", a.Auth.Require("vendor.create"), a.createVendor)
	r.PUT("/api/v1/admin/vendors/:id", a.Auth.Require("vendor.update"), a.updateVendor)
	r.GET("/api/v1/admin/vendor-options", a.Auth.Require("device.import"), a.vendorOptions)
}

// listVendors 分页查询厂商，支持启用或禁用状态筛选。
func (a API) listVendors(c *gin.Context) { a.listVendorPage(c, false) }

// vendorOptions 返回当前管理员范围内的启用厂商，供设备导入选择。
func (a API) vendorOptions(c *gin.Context) { a.listVendorPage(c, true) }

// listVendorPage 按当前管理员范围分页查询厂商；options 为 true 时仅返回导入可选项。
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

// getVendor 读取当前管理员范围内的指定厂商。
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

// createVendor 创建厂商并返回网关回复。
func (a API) createVendor(c *gin.Context) { a.saveVendor(c, 0) }

// updateVendor 更新指定厂商并返回网关回复。
func (a API) updateVendor(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	a.saveVendor(c, id)
}

// saveVendor 校验厂商资料和管理员当前范围；id 为零时新建，否则更新。
func (a API) saveVendor(c *gin.Context, id uint64) {
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	// 保留受限创建先拒绝范围、再解析输入的响应顺序。
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

// VendorFailure 把 err 的公开业务码、范围缺失或权限写入 c；其他异常统一依赖不可用，不透传认证错误。
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
