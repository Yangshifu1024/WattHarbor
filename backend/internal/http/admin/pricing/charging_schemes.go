package pricing

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	devstore "wattharbor/internal/device"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/http/admin"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/pricing"
)

// registerChargingSchemes 注册方案模板、发布、继承、预览和执行能力接口，并校验各自权限。
func (a API) registerChargingSchemes(r *gin.Engine) {
	p := "/api/v1/admin/settings/charging-schemes"
	r.GET(p, a.Auth.Require("pricing.read"), a.chargingSchemes)
	r.POST(p, a.Auth.Require("pricing.rule.create"), a.saveChargingScheme)
	r.PUT(p+"/:id", a.Auth.Require("pricing.rule.update"), a.saveChargingScheme)
	r.POST(p+"/:id/status", a.Auth.Require("pricing.rule.update"), a.chargingSchemeStatus)
	r.DELETE(p+"/:id", a.Auth.Require("pricing.rule.update"), a.deleteChargingScheme)
	r.POST(p+"/apply", a.Auth.Require("pricing.rule.create"), a.applyChargingScheme)
	r.POST(p+"/preview", a.Auth.Require("pricing.read"), a.previewChargingScheme)
	r.GET("/api/v1/admin/stations/:id/charging-scheme", a.Auth.Require("pricing.read"), a.effectiveChargingScheme)
	r.POST("/api/v1/admin/stations/:id/charging-scheme/inherit", a.Auth.Require("pricing.rule.update"), a.inheritChargingScheme)
	r.GET("/api/v1/admin/settings/device-capabilities", a.Auth.Require("pricing.read"), a.executionCapabilities)
}

// asPricingConflict 将 err 中的方案版本冲突转为后台 409 对应的错误类型，其余错误原样返回。
func asPricingConflict(err error) error {
	if errors.Is(err, pricing.ErrPricingVersionConflict) {
		return admin.ErrConflict
	}
	return err
}

// chargingSchemes 查询方案模板并返回当前管理员的权限；跳过无法解析的历史模板。
func (a API) chargingSchemes(c *gin.Context) {
	items, err := a.Queries.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": items, "permissions": c.MustGet("admin_profile").(operations.Profile).Permissions})
}

// saveChargingScheme 创建或更新方案模板，返回主键和版本。
func (a API) saveChargingScheme(c *gin.Context) {
	var in pricing.SchemeInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	var id uint64
	if c.Request.Method == "PUT" {
		var ok bool
		id, ok = admin.AdminPathID(c)
		if !ok {
			return
		}
	}
	out, err := a.Schemes.Save(c.Request.Context(), pricingAudit(c), id, in)
	if err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// chargingSchemeStatus 启用或停用指定方案模板。
func (a API) chargingSchemeStatus(c *gin.Context) { a.changeChargingScheme(c, false) }

// deleteChargingScheme 软删除指定方案模板。
func (a API) deleteChargingScheme(c *gin.Context) { a.changeChargingScheme(c, true) }

// changeChargingScheme 校验版本和变更输入，再启停或软删除方案模板。
func (a API) changeChargingScheme(c *gin.Context, remove bool) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	var in pricing.ChangeSchemeInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if err := a.Schemes.ChangeStatus(c.Request.Context(), pricingAudit(c), id, in, remove); err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// applyChargingScheme 按管理员当前站点范围发布方案。
func (a API) applyChargingScheme(c *gin.Context) {
	var in pricing.PublishInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	// 先校验格式，维持非法输入不会查询数据库范围的顺序。
	if uuid.Validate(in.RequestID) != nil || in.StationID == 0 || in.DeviceID != "" && !devstore.ValidDeviceID(in.DeviceID) || (in.TemplateID == 0) == (in.Scheme == nil) {
		httpapi.BadRequest(c, "请选择模板或完整方案，并提供请求编号与站点")
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Publications.Publish(c.Request.Context(), pricingAudit(c), scope, in)
	if err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// effectiveChargingScheme 读取指定站点或设备当前生效的方案；没有规则时返回空方案。
func (a API) effectiveChargingScheme(c *gin.Context) {
	station, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Queries.Effective(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), scope, station, c.Query("device_id"))
	if err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// inheritChargingScheme 让指定设备继承站点方案。
func (a API) inheritChargingScheme(c *gin.Context) {
	station, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	var in pricing.InheritInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if in.DeviceID == "" || !devstore.ValidDeviceID(in.DeviceID) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	if err := a.Publications.Assign(c.Request.Context(), pricingAudit(c), scope, station, in); err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"inherited": true})
}

// previewChargingScheme 根据请求中的完整方案计算预览，不读取数据库。
func (a API) previewChargingScheme(c *gin.Context) {
	var in struct {
		Scheme      charging.Scheme      `json:"scheme"`       // 完整待预览方案，不持久保存
		PackageID   uint64               `json:"package_id"`   // 选中的方案内套餐
		Meter       charging.ActualMeter `json:"meter"`        // 模拟计量，Wh／秒及起止时刻
		Scenario    string               `json:"scenario"`     // 领域支持的预览场景标签
		WalletCents int64                `json:"wallet_cents"` // 模拟可用余额，分
	}
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	result, err := in.Scheme.PreviewScenario(in.PackageID, in.Meter, in.Scenario, in.WalletCents)
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	httpapi.OK(c, result)
}

// executionCapabilities 返回指定设备的协议适配器支持的充电能力。
func (a API) executionCapabilities(c *gin.Context) {
	station, ok := admin.QueryStationID(c, true)
	if !ok {
		return
	}
	device := c.Query("device_id")
	if !devstore.ValidDeviceID(device) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Queries.Capabilities(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), scope, station, device)
	if err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// pricingAudit 从请求取得已鉴权的管理员、客户端 IP 和请求编号。
func pricingAudit(c *gin.Context) ops.AuditContext {
	return ops.AuditContext{Actor: c.MustGet("admin_profile").(operations.Profile), ClientIP: c.ClientIP(), RequestID: httpapi.RequestID(c)}
}

// pricingFailure 将输入、权限、版本及存储错误映射为接口错误；不暴露底层存储信息。
func pricingFailure(c *gin.Context, err error) {
	var input pricing.InputError
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Error())
	case errors.Is(err, operations.ErrForbidden):
		httpapi.Write(c, 403, 1003, err.Error(), nil)
	default:
		admin.AdminResourceFailure(c, asPricingConflict(err))
	}
}
