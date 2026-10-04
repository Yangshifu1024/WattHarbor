package dashboard

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"time"
)

// Dashboard 从 central_db 查询订单、用户、站点、设备和告警指标，仅持有数据库连接。
type Dashboard struct{ DB *gorm.DB }

// Metrics 与 TrendDay 是看板的响应行，指标汇总的查询在持久化层。
type Metrics = database.DashboardMetrics

type TrendDay = database.DashboardTrendDay

// Read 汇总首页看板指标。
func (d Dashboard) Read(ctx context.Context, now time.Time) (Metrics, error) {
	return (database.DashboardStore{DB: d.DB}).Read(ctx, now)
}

// Register 注册 GET /api/v1/admin/dashboard，需要 dashboard.read 权限；
// 读取失败时返回 503 而不是空指标，避免看板被误读成“业务正常”。
func (d Dashboard) Register(r *gin.Engine, a chargehttp.AdminAuth) {
	r.GET("/api/v1/admin/dashboard", a.Require("dashboard.read"), func(c *gin.Context) {
		m, err := d.Read(c.Request.Context(), time.Now())
		if err != nil {
			httpapi.Write(c, 503, 5003, "仪表盘数据暂不可用", nil)
			return
		}
		httpapi.OK(c, m)
	})
}
