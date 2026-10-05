package ops

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 首页看板的指标汇总。业务日界按北京时间切：库里的时间统一存 UTC，
// 自然日边界由查询服务计算，仓储只接收已换算的UTC区间。
//
// 全部指标通过同一个 central_db 连接池读取。

// DashboardTrendDay 是趋势图的一格，按东八区自然日聚合已结束的充电订单。
type DashboardTrendDay struct {
	Day             string `json:"day"`              // 统计日期,格式 YYYY-MM-DD,已按东八区换算
	CompletedOrders int64  `json:"completed_orders"` // 当天已结束(ended_at 非空且已计费)的充电订单数
	SettledCents    int64  `json:"settled_cents"`    // 当天已结束订单的合计金额,单位分
}

// DashboardMetrics 是一次看板快照。任一查询失败都返回错误，避免展示不完整数据
// 被误读成"业务正常"。
type DashboardMetrics struct {
	ChargingOrders       int64               `json:"charging_orders"`        // 当前状态为 charging(充电中)的订单数
	TodayOrders          int64               `json:"today_orders"`           // 今日(北京时间)创建的未删除订单数，包含未启动订单
	TotalUsers           int64               `json:"total_users"`            // 未删除的充电用户总数
	NewUsers             int64               `json:"new_users"`              // 今日(北京时间)注册的未删除充电用户数
	TodayChargingUsers   int64               `json:"today_charging_users"`   // 今日(北京时间)实际开始过充电的去重用户数，按 started_at 统计
	StationCount         int64               `json:"station_count"`          // 未删除的站点数
	DeviceCount          int64               `json:"device_count"`           // 未删除的设备元数据数
	TodayOrderUsers      int64               `json:"today_order_users"`      // 今日(东八区)创建过订单的去重用户数
	TodayCompletedOrders int64               `json:"today_completed_orders"` // 今日已结束订单数,取自趋势的最后一格
	TodaySettledCents    int64               `json:"today_settled_cents"`    // 今日已结束订单的合计金额,单位分
	ActiveAlerts         int64               `json:"active_alerts"`          // 未处理告警数：active 与 acknowledged 两种状态
	DailyTrend           []DashboardTrendDay `json:"daily_trend"`            // 最近 7 天(含今天)的趋势,按天升序固定 7 格,没有数据的天补 0
	UpdatedAt            time.Time           `json:"updated_at"`             // 本次快照的生成时间(UTC)
}

// DashboardRepo 汇总首页看板指标。
type DashboardRepo struct {
	DB *gorm.DB // 同一central数据库的跨模块只读投影
}

// Read 在ctx下读取[start,end)的当日事实与[trendStart,end)的按日趋势，返回投影或取消／存储错误。
// 仅访问各模块原事实，不写表、不补造未计费金额；任一查询失败不能把部分指标作为完整快照。
func (s DashboardRepo) Read(ctx context.Context, start, end, trendStart time.Time) (DashboardMetrics, error) {
	m := DashboardMetrics{DailyTrend: []DashboardTrendDay{}}

	orders := s.DB.WithContext(ctx).Table("charge_order").Where("deleted_at IS NULL")
	if err := applyCount(orders, &m.ChargingOrders, "status = 'charging'"); err != nil {
		return m, err
	}
	if err := applyCount(orders, &m.TodayOrders, "created_at >= ? AND created_at < ?", start, end); err != nil {
		return m, err
	}
	if err := applyDistinctCount(orders, &m.TodayOrderUsers, "created_at >= ? AND created_at < ?", start, end); err != nil {
		return m, err
	}
	if err := applyDistinctCount(orders, &m.TodayChargingUsers, "started_at >= ? AND started_at < ?", start, end); err != nil {
		return m, err
	}

	users := s.DB.WithContext(ctx).Table("user").Where("deleted_at IS NULL")
	if err := applyCount(users, &m.TotalUsers, ""); err != nil {
		return m, err
	}
	if err := applyCount(users, &m.NewUsers, "created_at >= ? AND created_at < ?", start, end); err != nil {
		return m, err
	}

	admin := s.DB.WithContext(ctx)
	for _, c := range []struct {
		table string // 已知来源表
		into  *int64 // 对应只读指标
	}{{"station", &m.StationCount}, {"device_meta", &m.DeviceCount}} {
		if err := admin.Table(c.table).Where("deleted_at IS NULL").Count(c.into).Error; err != nil {
			return m, err
		}
	}

	// 趋势按东八区自然日分组：ended_at 存的是 UTC，加 8 小时再取日期。
	rows := []DashboardTrendDay{}
	err := orders.Session(&gorm.Session{}).
		Select("DATE_FORMAT(DATE_ADD(ended_at, INTERVAL 8 HOUR), '%Y-%m-%d') AS day, "+
			"COUNT(*) AS completed_orders, COALESCE(SUM(total_cents),0) AS settled_cents").
		Where("ended_at >= ? AND ended_at < ? AND total_cents IS NOT NULL", trendStart, end).
		Group("day").Find(&rows).Error
	if err != nil {
		return m, err
	}
	m.DailyTrend = rows

	err = admin.Table("alert_event").Where("status IN ('active','acknowledged')").Count(&m.ActiveAlerts).Error
	return m, err
}

// applyCount 在既有查询上加一个计数条件。where 为空表示直接用基础查询计数。
// 每个条件都开新 session，避免上一次的 Where 累积到下一次。
//
// args 必须与 where 里的占位符一一对应：给没有占位符的条件多传参数会让
// gorm 报参数个数不匹配，看板整块 503。
func applyCount(base *gorm.DB, into *int64, where string, args ...any) error {
	query := base.Session(&gorm.Session{})
	if where != "" {
		query = query.Where(where, args...)
	}
	return query.Count(into).Error
}

// applyDistinctCount 在基础查询的独立session中按where和args统计user_id，返回计数或存储错误。
func applyDistinctCount(base *gorm.DB, into *int64, where string, args ...any) error {
	return base.Session(&gorm.Session{}).Where(where, args...).Distinct("user_id").Count(into).Error
}
