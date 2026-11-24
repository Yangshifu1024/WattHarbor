package ops

import (
	"context"
	"gorm.io/gorm"
	"time"
	"wattharbor/internal/domain/operations"
)

// dashboardBeijing 固定北京时间日界，不依赖进程或数据库时区。
var dashboardBeijing = time.FixedZone("Asia/Shanghai", 8*3600)

// DashboardQueryService 校验看板权限、换算北京时间区间并补齐七天趋势。
type DashboardQueryService struct {
	DB *gorm.DB /* 中央看板数据库连接 */
}

// Read 核对 dashboard.read 权限后读取当日和七日指标，任一查询失败即拒绝返回。
// 多个查询不保证同一事务快照；费用只取已保存的结算金额。
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
