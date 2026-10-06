package ops

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
	"time"
)

// dashboardBeijing 固定东八区业务日界，不依赖进程所在地或数据库连接时区。
var dashboardBeijing = time.FixedZone("Asia/Shanghai", 8*3600)

// DashboardQueryService 拥有看板权限、北京时间窗口和七格补零，只读跨模块原事实。
type DashboardQueryService struct {
	DB *gorm.DB /* 同一central数据库的看板投影 */
}

// Read 在ctx下核对actor当前dashboard.read权限，按now读取原当日与七日指标，返回完整快照或错误。
// 多条只读SQL不宣称同一数据库事务快照；任一失败都拒绝输出。费用仅取已有结算，不补造确认金额。
func (s DashboardQueryService) Read(ctx context.Context, actor operations.Profile, now time.Time) (DashboardMetrics, error) {
	if _, err := readManagementActor(ctx, s.DB, actor, "dashboard.read"); err != nil {
		return DashboardMetrics{}, err
	}
	local := now.In(dashboardBeijing)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, dashboardBeijing)
	m, err := (DashboardRepo{DB: s.DB}).Read(ctx, today.UTC(), today.AddDate(0, 0, 1).UTC(), today.AddDate(0, 0, -6).UTC())
	if err != nil {
		return DashboardMetrics{}, err
	}
	byDay := make(map[string]DashboardTrendDay, len(m.DailyTrend))
	for _, row := range m.DailyTrend {
		byDay[row.Day] = row
	}
	m.DailyTrend = make([]DashboardTrendDay, 0, 7)
	for i := -6; i <= 0; i++ {
		day := today.AddDate(0, 0, i).Format("2006-01-02")
		row := byDay[day]
		row.Day = day
		m.DailyTrend = append(m.DailyTrend, row)
		if i == 0 {
			m.TodayCompletedOrders = row.CompletedOrders
			m.TodaySettledCents = row.SettledCents
		}
	}
	m.UpdatedAt = now.UTC()
	return m, nil
}
