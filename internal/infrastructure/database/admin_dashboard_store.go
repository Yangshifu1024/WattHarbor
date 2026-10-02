package database

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 首页看板的指标汇总。业务日界按北京时间切：库里的时间统一存 UTC，
// 跨时区的换算集中在 Read 里的 local，不散落到各个查询。
//
// 订单与用户在 central_db，站点、设备与告警也在 central_db，但走不同的连接
// 句柄，与订单侧保持一致，避免同一次快照跨库读到不同时刻。

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

// DashboardStore 汇总首页看板指标。
type DashboardStore struct {
	UserDB, AdminDB *gorm.DB
}

// beijing 是业务日界使用的固定时区。库里的时间存 UTC，"今天"按北京时间切。
var beijing = time.FixedZone("Asia/Shanghai", 8*3600)

// Read 先按东八区切出"今天"，再回查最近 7 天（含今天）的趋势，
// 并把今天的格子顺带填进今日指标，避免为同一个数字再查一次库。
func (s DashboardStore) Read(ctx context.Context, now time.Time) (DashboardMetrics, error) {
	local := now.In(beijing)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	start, end := today.UTC(), today.AddDate(0, 0, 1).UTC()
	m := DashboardMetrics{UpdatedAt: now.UTC(), DailyTrend: make([]DashboardTrendDay, 0, 7)}

	orders := s.UserDB.WithContext(ctx).Table("charge_order").Where("deleted_at IS NULL")
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

	users := s.UserDB.WithContext(ctx).Table("user").Where("deleted_at IS NULL")
	if err := applyCount(users, &m.TotalUsers, ""); err != nil {
		return m, err
	}
	if err := applyCount(users, &m.NewUsers, "created_at >= ? AND created_at < ?", start, end); err != nil {
		return m, err
	}

	admin := s.AdminDB.WithContext(ctx)
	for _, c := range []struct {
		table string
		into  *int64
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
		Where("ended_at >= ? AND ended_at < ? AND total_cents IS NOT NULL", today.AddDate(0, 0, -6).UTC(), end).
		Group("day").Find(&rows).Error
	if err != nil {
		return m, err
	}
	byDay := map[string]DashboardTrendDay{}
	for _, row := range rows {
		byDay[row.Day] = row
	}
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

// applyDistinctCount 统计去重用户数。
func applyDistinctCount(base *gorm.DB, into *int64, where string, args ...any) error {
	return base.Session(&gorm.Session{}).Where(where, args...).Distinct("user_id").Count(into).Error
}
