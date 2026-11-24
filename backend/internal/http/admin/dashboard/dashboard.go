// Package dashboard 提供后台经营看板接口。
package dashboard

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/domain/operations"
	adminhttp "wattharbor/internal/http/admin"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// Dashboard 只持有看板服务，不组装查询或业务日界。
type Dashboard struct {
	Queries ops.DashboardQueryService /* 跨模块只读组合 */
}

// Register 在 r 挂载 GET 看板路径，以 a 提取当前身份，返回完整指标或 503，不启动后台任务。
func (d Dashboard) Register(r *gin.Engine, a authhttp.AdminAuth) {
	r.GET("/api/v1/admin/dashboard", a.Require("dashboard.read"), func(c *gin.Context) {
		m, err := d.Queries.Read(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), time.Now())
		if err != nil {
			// 请求鉴权与服务重读之间仍可能撤权或提升版本；鉴权错误保留 401／403。
			if errors.Is(err, operations.ErrCredentials) || errors.Is(err, ops.ErrPermission) {
				adminhttp.AdminManagementFailure(c, err)
				return
			}
			httpapi.Write(c, 503, 5003, "仪表盘数据暂不可用", nil)
			return
		}
		httpapi.OK(c, m)
	})
}
