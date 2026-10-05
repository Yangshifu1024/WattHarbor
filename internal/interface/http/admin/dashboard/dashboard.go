package dashboard

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"time"
)

// Dashboard 只持有所属看板服务，不组装查询或业务日界。
type Dashboard struct {
	Queries ops.DashboardQueryService /* 所属跨模块只读组合 */
}

// Register 在r挂载原GET看板路径，以a提取当前身份，返回完整指标或原503，不启动后台任务。
func (d Dashboard) Register(r *gin.Engine, a chargehttp.AdminAuth) {
	r.GET("/api/v1/admin/dashboard", a.Require("dashboard.read"), func(c *gin.Context) {
		m, err := d.Queries.Read(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), time.Now())
		if err != nil {
			// 请求鉴权与服务重读之间仍可能撤权或提升版本；准入错误保留401／403。
			if errors.Is(err, operations.ErrCredentials) || errors.Is(err, ops.ErrPermission) {
				chargehttp.AdminManagementFailure(c, err)
				return
			}
			httpapi.Write(c, 503, 5003, "仪表盘数据暂不可用", nil)
			return
		}
		httpapi.OK(c, m)
	})
}
