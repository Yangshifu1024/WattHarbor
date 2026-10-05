package pricing

import (
	"errors"
	devstore "github.com/ChargePilot2026/charge-pilot/internal/device"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// registerChargingSchemes 在 r 上绑定原方案路由及逐动作权限，只负责 HTTP 装配。
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

// asPricingConflict 把持久化层的版本冲突哨兵转成后台统一的业务冲突，
// 让「别人刚改过这一版」返回 409 而不是 500。
func asPricingConflict(err error) error {
	if errors.Is(err, pricing.ErrPricingVersionConflict) {
		return admin.ErrConflict
	}
	return err
}

// chargingSchemes 在请求 context 下读取模板并输出原列表及实时权限；非法历史 JSON 沿用原行为跳过。
// 存储错误写统一失败响应，不执行业务写入。
func (a API) chargingSchemes(c *gin.Context) {
	items, err := a.Queries.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": items, "permissions": c.MustGet("admin_profile").(operations.Profile).Permissions})
}

// saveChargingScheme 解码公开输入及路径，将保存交给注入用例，返回原主键和版本。
func (a API) saveChargingScheme(c *gin.Context) {
	var in pricing.SchemeInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	var id uint64
	if c.Request.Method == "PUT" {
		var ok bool
		id, ok = chargehttp.AdminPathID(c)
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

// chargingSchemeStatus 将 c 中启停请求交给统一变更适配器，无独立事务。
func (a API) chargingSchemeStatus(c *gin.Context) { a.changeChargingScheme(c, false) }

// deleteChargingScheme 将 c 中软删除请求交给统一变更适配器，不删除历史规则。
func (a API) deleteChargingScheme(c *gin.Context) { a.changeChargingScheme(c, true) }

// changeChargingScheme 解码原启停／删除输入；remove 指定软删除，事务与审计由用例管理。
func (a API) changeChargingScheme(c *gin.Context, remove bool) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in pricing.ChangeSchemeInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if err := a.Schemes.ChangeStatus(c.Request.Context(), pricingAudit(c), id, in, remove); err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// applyChargingScheme 解码原发布请求和实时范围，调用唯一发布入口并返回原结果。
func (a API) applyChargingScheme(c *gin.Context) {
	var in pricing.PublishInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	// 先校验格式，维持非法输入不会查询数据库范围的原顺序。
	if uuid.Validate(in.RequestID) != nil || in.StationID == 0 || in.DeviceID != "" && !devstore.ValidDeviceID(in.DeviceID) || (in.TemplateID == 0) == (in.Scheme == nil) {
		httpapi.BadRequest(c, "请选择模板或完整方案，并提供请求编号与站点")
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
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

// effectiveChargingScheme 在 c 下解析目标及实时范围，读取原当前规则和设备覆盖版本。
// 无规则返回空方案；缺归属或存储错误写原错误响应，不以历史冻结规则替代当前方案。
func (a API) effectiveChargingScheme(c *gin.Context) {
	station, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
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

// inheritChargingScheme 解码设备继承输入及实时范围，返回原继承标志或统一错误。
func (a API) inheritChargingScheme(c *gin.Context) {
	station, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in pricing.InheritInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.DeviceID == "" || !devstore.ValidDeviceID(in.DeviceID) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	if err := a.Publications.Assign(c.Request.Context(), pricingAudit(c), scope, station, in); err != nil {
		pricingFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"inherited": true})
}

// previewChargingScheme 解码 c 提供的完整场景，调用领域纯计算，返回预览或格式错误，不访问数据库。
func (a API) previewChargingScheme(c *gin.Context) {
	var in struct {
		Scheme      charging.Scheme      `json:"scheme"`       // 完整待预览方案，不持久保存
		PackageID   uint64               `json:"package_id"`   // 选中的方案内套餐
		Meter       charging.ActualMeter `json:"meter"`        // 模拟计量，Wh／秒及起止时刻
		Scenario    string               `json:"scenario"`     // 领域支持的预览场景标签
		WalletCents int64                `json:"wallet_cents"` // 模拟可用余额，分
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	result, err := in.Scheme.PreviewScenario(in.PackageID, in.Meter, in.Scenario, in.WalletCents)
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	httpapi.OK(c, result)
}

// executionCapabilities 在 c 下校验查询目标与实时范围，输出所选适配器的领域能力或原失败响应。
// 不按设备型号推测协议，不读取或修改资金。
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
	scope, ok := admin.StationScope(c, a.DB)
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

// pricingAudit 提取可信认证和请求事实，不持有 gin 的业务事务依赖。
func pricingAudit(c *gin.Context) ops.AuditContext {
	return ops.AuditContext{Actor: c.MustGet("admin_profile").(operations.Profile), ClientIP: c.ClientIP(), RequestID: httpapi.RequestID(c)}
}

// pricingFailure 将输入、权限、版本及存储错误映射为原接口错误；不暴露底层存储信息。
func pricingFailure(c *gin.Context, err error) {
	var input pricing.InputError
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Error())
	case errors.Is(err, operations.ErrForbidden):
		httpapi.Write(c, 403, 1003, err.Error(), nil)
	default:
		chargehttp.AdminResourceFailure(c, asPricingConflict(err))
	}
}
