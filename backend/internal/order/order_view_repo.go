package order

import (
	"context"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"strings"
	"time"
)

// 后台订单的列表与详情读模型。订单自身不含金额去向，金额怎么分在
// central_db，由详情接口按订单号二次查询后挂上。

// OrderView 是后台订单列表与详情共用的行模型。
//
// 数据主体是 central_db 的充电订单，独立读取持久化业务、支付状态，
// 并联出支付单的实际金额、退款进度和支付意图的站点资料。
type OrderView struct {
	Live                *charging.LiveMeterView `json:"live,omitempty" gorm:"-"`             // 原计量估算视图，不等于最终结算，缺失为空
	LiveUnavailable     string                  `json:"live_unavailable,omitempty" gorm:"-"` // 实时读取失败的原公开提示，正常为空
	OrderID             uint64                  `json:"order_id"`                            // 充电订单主键
	OrderNo             string                  `json:"order_no"`                            // 业务订单号，对外展示和排障都用它
	UserID              uint64                  `json:"user_id,string"`                      // 下单用户 ID
	DeviceID            string                  `json:"device_id"`                           // 设备 ID
	PortNo              uint8                   `json:"port_no"`                             // 充电枪序号
	StationID           *uint64                 `json:"station_id"`                          // 所属站点 ID；指针，关联不上时为 null
	StationName         *string                 `json:"station_name" gorm:"-"`               // 站点名称；gorm:"-" 表示不参与扫描，由 FillOrderStations 二次回填
	Status              string                  `json:"status"`                              // 订单状态：pending_payment/paid/charging/completed/cancelled/failed/refunding/refunded
	BusinessStatus      string                  `json:"business_status"`                     // 持久化业务状态：pending_start/charging/completed。
	PaymentStatus       string                  `json:"payment_status"`                      // 持久化支付状态：pending/paid/refunded/partial_refunded。
	StartSource         *string                 `json:"start_source"`                        // payment 扫码支付、balance 余额支付、card 在线卡；缺失来源时为空。
	SelectedSchemeName  *string                 `json:"selected_scheme_name" gorm:"-"`       // 下单时冻结的计费方案名称；缺失快照或有效名称时为 null。
	SelectedPackageName *string                 `json:"selected_package_name" gorm:"-"`      // 下单时冻结的套餐名称；缺失有效套餐名称时为 null。
	CreatedAt           time.Time               `json:"created_at"`                          // 订单创建时间
	StartedAt           *time.Time              `json:"started_at"`                          // 实际开始充电时间；指针，未开始为 null
	EndedAt             *time.Time              `json:"ended_at"`                            // 结束充电时间；指针，未结束为 null
	DurationSeconds     *uint64                 `json:"duration_seconds"`                    // 计费时长（秒）；指针，计费完成后才有
	MeterKWh            *string                 `json:"meter_kwh" gorm:"column:meter_kwh"`   // 电表读数（kWh）；用字符串承载 DECIMAL，避免浮点丢精度
	ElectricFeeCents    *int64                  `json:"electric_fee_cents"`                  // 电费（分）；指针，尚未计费为 null
	ServiceFeeCents     *int64                  `json:"service_fee_cents"`                   // 服务费（分）；指针，尚未计费为 null
	TotalFeeCents       *int64                  `json:"total_fee_cents"`                     // 应收合计（分）；指针，尚未计费为 null
	RefundStatus        string                  `json:"refund_status"`                       // 退款进度：none/processing/partial_refunded/refunded，由 SQL 依据支付单与退款单算出
	PaymentOrderID      *uint64                 `json:"payment_order_id"`                    // 关联支付单主键；指针，未发起支付为 null
	PaymentOrderNo      *string                 `json:"payment_order_no"`                    // 支付单号；指针，未发起支付为 null
	PaymentOrderStatus  *string                 `json:"payment_order_status"`                // 支付单内部流程状态；未关联有效支付单时为 null。
	PaidCents           *int64                  `json:"paid_cents"`                          // 实收金额（分）；指针，未支付为 null
	RefundedCents       *int64                  `json:"refunded_cents"`                      // 已成功退款金额（分）；有效支付单未退款为 0，缺失支付单为 null。
	FailureReason       *string                 `json:"failure_reason"`                      // 失败原因；指针，非失败单为 null
	RefundApplicantID   *string                 `json:"refund_applicant_id" gorm:"-"`        // 退款操作人展示字段；本查询不加载，原公开值保持 null
	Billing             *OrderBilling           `json:"billing" gorm:"-"`                    // 计费标识；不在本查询中加载，仅详情接口填充
	SelectedPackage     *charging.OrderPackage  `json:"selected_package" gorm:"-"`           // 下单时冻结的套餐及规则，仅详情接口填充
}

// OrderBilling 是订单详情附带的计费标识。订单本身不含金额去向，费用在
// 结算后才有结果，这里只带回计费单号。
type OrderBilling struct {
	CalculationNo *string `json:"calculation_no"` // 计费单号；指针，尚未完成计费为 null
}

// OrderQuery 是订单列表的查询条件。除 Query 外的字段都是可选筛选项，零值表示不过滤。
type OrderQuery struct {
	persist.PageQuery                                     // 复用通用分页与状态白名单校验
	OrderNo, DeviceID                          string     // 按订单号 / 设备号精确匹配
	BusinessStatus, PaymentStatus, StartSource string     // 业务、支付状态与启动来源独立筛选。
	StationID                                  uint64     // 按站点过滤，0 表示不限
	From, To                                   *time.Time // 充电时间区间（闭区间）；指针，某一端为 nil 表示该端不限
}

// orderStartSourceSQL 仅用于固定订单／支付别名 c/p 的原启动来源投影，卡记录优先。
const orderStartSourceSQL = `(CASE WHEN EXISTS (SELECT 1 FROM card_charge cc WHERE cc.charge_order_id=c.id) THEN 'card' WHEN p.pay_method='balance' THEN 'balance' WHEN p.pay_method='wechat' THEN 'payment' ELSE NULL END)`

// orderColumns 固定后台订单投影列，金额与退款进度只读取已持久化事实。
const orderColumns = `c.id AS order_id,c.order_no,c.user_id,c.device_id,c.port_no,i.station_id,c.status,c.business_status,c.payment_status,` + orderStartSourceSQL + ` AS start_source,c.created_at,c.started_at,c.ended_at,
 c.charged_seconds AS duration_seconds,c.charged_kwh AS meter_kwh,c.electric_cents AS electric_fee_cents,c.service_cents AS service_fee_cents,c.total_cents AS total_fee_cents,
 c.payment_order_id,p.order_no AS payment_order_no,p.status AS payment_order_status,p.paid_cents,p.refunded_cents,c.failure_reason,
 CASE WHEN EXISTS (SELECT 1 FROM refund_record r WHERE r.payment_order_id=p.id AND r.status IN ('pending','processing') AND r.deleted_at IS NULL) THEN 'processing' WHEN c.payment_status='refunded' THEN 'refunded' WHEN c.status='refunding' THEN 'processing' WHEN p.refunded_cents>0 THEN 'partial_refunded' ELSE 'none' END AS refund_status`

// orderBaseQuery 在 ctx 下用 db 构造包内固定订单／支付／意图只读投影，排除软删除。
// 返回包内 builder，不执行 IO 或写入；完整分页与详情由所属 repo 执行，不向其它业务暴露 builder。
func orderBaseQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("charge_order AS c").Joins("LEFT JOIN payment_order AS p ON p.id=c.payment_order_id AND p.user_id=c.user_id AND p.deleted_at IS NULL").Joins("LEFT JOIN charge_payment_intent AS i ON i.charge_order_id=c.id").Where("c.deleted_at IS NULL")
}

// ListOrders 在 ctx 下用 db 按 q 已校验条件分页读取原订单，返回原页或读取错误，无写入。
// 先 Count 出总数再按创建时间与主键倒序取当页，
// 最后回填站点名称。时间范围拆成实际启动、未启动两条索引条件，保留原 COALESCE 口径。
func ListOrders(ctx context.Context, db *gorm.DB, q OrderQuery) (persist.Page[OrderView], error) {
	out := persist.Page[OrderView]{Items: []OrderView{}, Page: q.Page, PageSize: q.PageSize}
	query := orderBaseQuery(ctx, db)
	if q.OrderNo != "" {
		query = query.Where("c.order_no = ?", q.OrderNo)
	}
	if q.DeviceID != "" {
		query = query.Where("c.device_id = ?", q.DeviceID)
	}
	if q.Status != "" {
		query = query.Where("c.status = ?", q.Status)
	}
	if q.BusinessStatus != "" {
		query = query.Where("c.business_status = ?", q.BusinessStatus)
	}
	if q.PaymentStatus != "" {
		query = query.Where("c.payment_status = ?", q.PaymentStatus)
	}
	if q.StartSource != "" {
		query = query.Where(orderStartSourceSQL+" = ?", q.StartSource)
	}
	if q.StationID != 0 {
		query = query.Where("i.station_id = ?", q.StationID)
	}
	query = orderTimeRange(query, q.From, q.To)
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Select(orderColumns).Order("c.created_at DESC,c.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error; err != nil {
		return out, err
	}
	if err := FillOrderStations(ctx, db, out.Items); err != nil {
		return out, err
	}
	return out, OrderSchemeNames(ctx, db, out.Items)
}

// orderTimeRange 按固定列构造启动优先、未启动回退创建时间的闭区间，所有时间均参数化。
// 不对列套函数，使两条范围分支可分别使用时间索引；不改变列表排序及总数口径。
func orderTimeRange(query *gorm.DB, from, to *time.Time) *gorm.DB {
	if from == nil && to == nil {
		return query
	}
	started := []string{"c.started_at IS NOT NULL"}
	created := []string{"c.started_at IS NULL"}
	args := []any{}
	for _, column := range []string{"c.started_at", "c.created_at"} {
		conditions := &started
		if column == "c.created_at" {
			conditions = &created
		}
		if from != nil {
			*conditions = append(*conditions, column+" >= ?")
			args = append(args, from.UTC())
		}
		if to != nil {
			*conditions = append(*conditions, column+" <= ?")
			args = append(args, to.UTC())
		}
	}
	return query.Where("(("+strings.Join(started, " AND ")+") OR ("+strings.Join(created, " AND ")+"))", args...)
}

// FillOrderStations 在 ctx 下用 db 按 rows 当页站点身份一次读取名称并原位回填，返回读取错误。
// 空关联不查询；缺失／已删除站点保留 nil，正常缺失不报错，无写入。
func FillOrderStations(ctx context.Context, db *gorm.DB, rows []OrderView) error {
	ids := []uint64{}
	for _, r := range rows {
		if r.StationID != nil {
			ids = append(ids, *r.StationID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// 只取 id 与 name 两列，不引入 device 包——这里要的是投影不是设备模型。
	var stations []struct {
		ID   uint64 // 原站点主键
		Name string // 当前站点名称，只读投影
	}
	if err := db.WithContext(ctx).Table("station").Select("id,name").Where("id IN ? AND deleted_at IS NULL", ids).Find(&stations).Error; err != nil {
		return err
	}
	names := map[uint64]string{}
	for _, r := range stations {
		names[r.ID] = r.Name
	}
	for i := range rows {
		if rows[i].StationID != nil {
			if name, ok := names[*rows[i].StationID]; ok {
				rows[i].StationName = &name
			}
		}
	}
	return nil
}
