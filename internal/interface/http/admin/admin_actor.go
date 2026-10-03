package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// RequireFinanceActor 要求当前账号是拥有指定权限的客户财务人员。
// 开票与计量复核都受这条约束，拆包后由 Deps 提供。
func (d Deps) RequireFinanceActor(c *gin.Context, permission string) (operations.Profile, bool) {
	p := c.MustGet("admin_profile").(operations.Profile)
	if p.Role != "customer_finance" || !p.HasPermission(permission) {
		httpapi.Write(c, 403, 1003, "须由拥有审核权限的客户财务人员执行", nil)
		return p, false
	}
	return p, true
}
