package database

import (
	"context"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
	"time"
)

// 后台订单的列表与详情读模型。订单自身不含金额去向，金额怎么分在
// central_db，由详情接口按订单号二次查询后挂上。

// OrderView 是后台订单列表与详情共用的行模型。
//
// 数据主体是 central_db 的充电订单，独立读取持久化业务、支付状态，
// 并联出支付单的实际金额、退款进度和支付意图的站点资料。
type OrderView struct {
	Live                *charging.LiveMeterView `json:"live,omitempty" gorm:"-"`
	LiveUnavailable     string                  `json:"live_unavailable,omitempty" gorm:"-"`
	OrderID             uint64                  `json:"order_id"`                          // 充电订单主键
	OrderNo             string                  `json:"order_no"`                          // 业务订单号，对外展示和排障都用它
	UserID              uint64                  `json:"user_id,string"`                    // 下单用户 ID
	DeviceID            string                  `json:"device_id"`                         // 设备 ID
	PortNo              uint8                   `json:"port_no"`                           // 充电枪序号
	StationID           *uint64                 `json:"station_id"`                        // 所属站点 ID；指针，关联不上时为 null
	StationName         *string                 `json:"station_name" gorm:"-"`             // 站点名称；gorm:"-" 表示不参与扫描，由 orderStations 二次回填
	Status              string                  `json:"status"`                            // 订单状态：pending_payment/paid/charging/completed/cancelled/failed/refunding/refunded
	BusinessStatus      string                  `json:"business_status"`                   // 持久化业务状态：pending_start/charging/completed。
	PaymentStatus       string                  `json:"payment_status"`                    // 持久化支付状态：pending/paid/refunded/partial_refunded。
	StartSource         *string                 `json:"start_source"`                      // payment 扫码支付、balance 余额支付、card 在线卡；缺失来源时为空。
	SelectedSchemeName  *string                 `json:"selected_scheme_name" gorm:"-"`     // 下单时冻结的计费方案名称；缺失快照或有效名称时为 null。
	SelectedPackageName *string                 `json:"selected_package_name" gorm:"-"`    // 下单时冻结的套餐名称；缺失有效套餐名称时为 null。
	CreatedAt           time.Time               `json:"created_at"`                        // 订单创建时间
	StartedAt           *time.Time              `json:"started_at"`                        // 实际开始充电时间；指针，未开始为 null
	EndedAt             *time.Time              `json:"ended_at"`                          // 结束充电时间；指针，未结束为 null
	DurationSeconds     *uint64                 `json:"duration_seconds"`                  // 计费时长（秒）；指针，计费完成后才有
	MeterKWh            *string                 `json:"meter_kwh" gorm:"column:meter_kwh"` // 电表读数（kWh）；用字符串承载 DECIMAL，避免浮点丢精度
	ElectricFeeCents    *int64                  `json:"electric_fee_cents"`                // 电费（分）；指针，尚未计费为 null
	ServiceFeeCents     *int64                  `json:"service_fee_cents"`                 // 服务费（分）；指针，尚未计费为 null
	TotalFeeCents       *int64                  `json:"total_fee_cents"`                   // 应收合计（分）；指针，尚未计费为 null
	RefundStatus        string                  `json:"refund_status"`                     // 退款进度：none/processing/partial_refunded/refunded，由 SQL 依据支付单与退款单算出
	PaymentOrderID      *uint64                 `json:"payment_order_id"`                  // 关联支付单主键；指针，未发起支付为 null
	PaymentOrderNo      *string                 `json:"payment_order_no"`                  // 支付单号；指针，未发起支付为 null
	PaymentOrderStatus  *string                 `json:"payment_order_status"`              // 支付单内部流程状态；未关联有效支付单时为 null。
	PaidCents           *int64                  `json:"paid_cents"`                        // 实收金额（分）；指针，未支付为 null
	RefundedCents       *int64                  `json:"refunded_cents"`                    // 已成功退款金额（分）；有效支付单未退款为 0，缺失支付单为 null。
	FailureReason       *string                 `json:"failure_reason"`                    // 失败原因；指针，非失败单为 null
	RefundApplicantID   *string                 `json:"refund_applicant_id" gorm:"-"`      // 发起退款的操作人 ID；gorm:"-" 表示不来自库表，只有具备 order.refund.create 权限时才回填，前端据此显示退款入口
	Billing             *OrderBilling           `json:"billing" gorm:"-"`                  // 分账信息；gorm:"-" 表示不在本查询中加载，仅详情接口填充
	SelectedPackage     *charging.OrderPackage  `json:"selected_package" gorm:"-"`         // 下单时冻结的套餐及规则，仅详情接口填充
}

// OrderBilling 是订单详情附带的结算视图。订单本身不含金额去向，金额怎么分在
// central_db，需要按订单号二次查询后挂在这里。
type OrderBilling struct {
	CalculationNo *string          `json:"calculation_no"` // 计费单号；指针，尚未完成计费为 null
	Settlements   []SettlementView `json:"settlements"`    // 该订单产生的分账汇总列表
}

// SettlementView 是 central_db 中一条分账汇总，映射自 settlement 表。
type SettlementView struct {
	SettlementID   uint64      `json:"settlement_id" gorm:"column:id"`                  // 分账汇总主键（表主键列名是 id）
	SettlementNo   string      `json:"settlement_no" gorm:"column:settlement_no"`       // 分账单号
	Mode           string      `json:"mode" gorm:"column:mode"`                         // 分账模式：mode_a / mode_b
	Status         string      `json:"status" gorm:"column:status"`                     // 分账状态：pending/confirmed/paid/failed
	SplitPoolCents int64       `json:"split_pool_cents" gorm:"column:split_pool_cents"` // 进入分账池的金额（分）
	Parties        []PartyView `json:"parties" gorm:"-"`                                // 各参与方金额；gorm:"-" 表示不参与扫描，由调用方逐条回填
}

// PartyView 是分账中的一个参与方及其应得金额，映射自 central_db.settlement_party_amount。
// 名称与比例都是结算当时的快照，之后改分账模板不会回溯这张表。
type PartyView struct {
	PartyID     uint64 `json:"party_id" gorm:"column:party_id"`         // 参与方 ID
	PartyCode   string `json:"party_code" gorm:"column:party_code"`     // 参与方编码
	PartyName   string `json:"party_name" gorm:"column:party_name"`     // 参与方名称（快照）
	RatioBP     uint32 `json:"ratio_bp" gorm:"column:ratio_bp"`         // 分成比例，万分比：10000 表示 100%
	AmountCents int64  `json:"amount_cents" gorm:"column:amount_cents"` // 该参与方应得金额（分）
	Status      string `json:"status" gorm:"column:status"`             // 打款状态：pending/paid/failed
}

// OrderQuery 是订单列表的查询条件。除 PageQuery 外的字段都是可选筛选项，零值表示不过滤。
type OrderQuery struct {
	PageQuery                                             // 复用通用分页与状态白名单校验
	OrderNo, DeviceID                          string     // 按订单号 / 设备号精确匹配
	BusinessStatus, PaymentStatus, StartSource string     // 业务、支付状态与启动来源独立筛选。
	StationID                                  uint64     // 按站点过滤，0 表示不限
	From, To                                   *time.Time // 充电时间区间（闭区间）；指针，某一端为 nil 表示该端不限
}

// OrderColumns 是订单列表与详情共用的查询列，两处口径必须一致。
// refund_status 不存在于任何一张表里，是按支付单状态和退款单状态在 SQL 里现算的。
const orderStartSourceSQL = `(CASE WHEN EXISTS (SELECT 1 FROM card_charge cc WHERE cc.charge_order_id=c.id) THEN 'card' WHEN p.pay_method='balance' THEN 'balance' WHEN p.pay_method='wechat' THEN 'payment' ELSE NULL END)`

const OrderColumns = `c.id AS order_id,c.order_no,c.user_id,c.device_id,c.port_no,i.station_id,c.status,c.business_status,c.payment_status,` + orderStartSourceSQL + ` AS start_source,c.created_at,c.started_at,c.ended_at,
 c.charged_seconds AS duration_seconds,c.charged_kwh AS meter_kwh,c.electric_cents AS electric_fee_cents,c.service_cents AS service_fee_cents,c.total_cents AS total_fee_cents,
 c.payment_order_id,p.order_no AS payment_order_no,p.status AS payment_order_status,p.paid_cents,p.refunded_cents,c.failure_reason,
 CASE WHEN EXISTS (SELECT 1 FROM refund_record r WHERE r.payment_order_id=p.id AND r.status IN ('pending','processing') AND r.deleted_at IS NULL) THEN 'processing' WHEN c.payment_status='refunded' THEN 'refunded' WHEN c.status='refunding' THEN 'processing' WHEN p.refunded_cents>0 THEN 'partial_refunded' ELSE 'none' END AS refund_status`

// orderQuery 关联支付单和支付意图，查询支付状态与站点归属，并排除软删除；投影和过滤由调用方补充。
func OrderBaseQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("charge_order AS c").Joins("LEFT JOIN payment_order AS p ON p.id=c.payment_order_id AND p.user_id=c.user_id AND p.deleted_at IS NULL").Joins("LEFT JOIN charge_payment_intent AS i ON i.charge_order_id=c.id").Where("c.deleted_at IS NULL")
}

// Orders 分页查询订单列表：先 Count 出总数再按创建时间倒序取当页，
// 最后回填站点名称。时间筛选用 COALESCE(started_at， created_at)，未开始充电的订单也能落进区间。
func ListOrders(ctx context.Context, db *gorm.DB, q OrderQuery) (Page[OrderView], error) {
	out := Page[OrderView]{Items: []OrderView{}, Page: q.Page, PageSize: q.PageSize}
	query := OrderBaseQuery(ctx, db)
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
	if q.From != nil {
		query = query.Where("COALESCE(c.started_at,c.created_at) >= ?", q.From.UTC())
	}
	if q.To != nil {
		query = query.Where("COALESCE(c.started_at,c.created_at) <= ?", q.To.UTC())
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Select(OrderColumns).Order("c.created_at DESC,c.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error; err != nil {
		return out, err
	}
	if err := FillOrderStations(ctx, db, out.Items); err != nil {
		return out, err
	}
	return out, OrderSchemeNames(ctx, db, out.Items)
}

// FillOrderStations 按订单行里的站点 ID 批量补上站点名称。
// 查不到已删除站点的行保留 nil，不报错。
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
	var stations []Station
	if err := db.WithContext(ctx).Select("id,name").Where("id IN ? AND deleted_at IS NULL", ids).Find(&stations).Error; err != nil {
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
