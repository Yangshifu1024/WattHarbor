package query

import (
	gatewaycontract "chargepilot/internal/contracts/gateway"
	carddomain "chargepilot/internal/domain/card"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/money"
	orderdomain "chargepilot/internal/domain/order"
	"chargepilot/internal/order"
	"context"
	"encoding/json"
	"time"
)

// UserOrders 只提供按用户隔离的订单，不允许调用方传入未经授权的数据库记录。
type UserOrders interface {
	// Resolve 核对 userID 对 raw 业务单号的归属，返回订单或输入、缺失、数据库错误。
	Resolve(ctx context.Context, userID uint64, raw string) (order.ChargeOrderRecord, error)
}

// UserStateReader 提供冻结与资金查询结果，不提供任意表名或可继续拼接的查询。
type UserStateReader interface {
	// Frozen 组合已授权 row 的冻结记录，返回状态。
	Frozen(ctx context.Context, row order.ChargeOrderRecord) (FrozenState, error)
	// Funds 读取已授权 row 的支付金额、状态和退款列表，不修改资金。
	Funds(ctx context.Context, row order.ChargeOrderRecord) (DetailFunds, error)
}

// UserCurveReader 提供设备端口的有界观测，不持有业务授权或改变订单状态。
type UserCurveReader interface {
	// Telemetry 读取 q 设备窗口，返回共享契约或网络、契约错误。
	Telemetry(ctx context.Context, q gatewaycontract.TelemetryRequest) (gatewaycontract.TelemetryReply, error)
	// HistoricalCurve 读取 q 订单聚合窗口，返回共享契约或网络、契约错误。
	HistoricalCurve(ctx context.Context, q gatewaycontract.HistoricalCurveRequest) (gatewaycontract.HistoricalCurveReply, error)
}

// UserLiveReader 提供已授权订单的实时估算，只填充公开查询结果，不写入结算费用。
type UserLiveReader interface {
	// Fill 为 rows 中充电订单补充观测与预估费用，失败保留不可读取提示。
	Fill(ctx context.Context, rows []order.OrderView)
}

// UserService 负责用户充电详情、状态补充和曲线窗口；确认金额、实时估价与遥测展示保持独立。
type UserService struct {
	Orders         UserOrders       // 所属订单隔离读取
	Fees           order.FeeReader  // 所属结算确认费用，不计算实时估价
	State          UserStateReader  // 冻结方案、计费进度、截止及资金摘要
	Gateway        UserCurveReader  // cmd 注入的网关曲线查询
	Live           UserLiveReader   // cmd 注入与后台使用相同规则的实时估算
	AllowedWindows []string         // 允许的既定回看窗口；空值使用三个默认窗口
	Now            func() time.Time // 可选受控时钟；空值使用当前 UTC 时刻
}

// UserReadError 保存失败查询的阶段和原因，供 HTTP 区分费用、支付、退款和曲线错误。
type UserReadError struct {
	Stage string // fee、state、payment、refund、curve或history_curve
	Cause error  // 所属读取错误
}

// Error 返回 e 的原始原因。
func (e *UserReadError) Error() string { return e.Cause.Error() }

// Unwrap 返回 e 的原始原因，供取消及缺行判断。
func (e *UserReadError) Unwrap() error { return e.Cause }

// UserInputError 是既定窗口校验结果，不包含他人订单信息。
type UserInputError struct {
	Kind    string // input非法窗口、conflict未开始、倒置窗口、expired超过保留期
	Message string // 公开错误文案
}

// Error 返回 e 的公开文案。
func (e *UserInputError) Error() string { return e.Message }

// CardOperation 定义接口返回的刷卡操作，包括扣款金额、购买时长与确认状态。
type CardOperation struct {
	OperationID   string                         `json:"operation_id"`    // 首次操作 UUID
	DeviceID      string                         `json:"device_id"`       // 设备编号
	EventID       string                         `json:"event_id"`        // 设备事件编号
	ChargeOrderID uint64                         `json:"charge_order_id"` // 充电订单主键
	CardID        uint64                         `json:"card_id"`         // 卡主键
	Kind          carddomain.CardOperationKind   `json:"kind"`            // start或extend
	PriceCents    int64                          `json:"price_cents"`     // 扣款金额（分）
	Minutes       uint16                         `json:"minutes"`         // 购买分钟数
	Status        carddomain.CardOperationStatus `json:"status"`          // 确认状态
}

// BillingCutoff 返回已冻结的计费截止时间；没有记录时为空，不重新计算时间。
type BillingCutoff struct {
	CutoffAt *string `json:"cutoff_at"` // 首次截止文本；无记录为空
	Reason   string  `json:"reason"`    // 截止原因；无记录为空串
}

// FrozenState 是对应模块只读组合，未保存快照时 Snapshot 为空。
type FrozenState struct {
	Snapshot       []byte          // 冻结 JSON，不读取当前模板
	BillingStatus  string          // 计费任务状态，无任务为空串
	CardOperations []CardOperation // 有序操作，无操作为空数组
	Cutoff         BillingCutoff   // 截止记录，无记录保持空值
}

// PaymentBrief 保留已确认支付金额，不携带 ORM 映射。
type PaymentBrief struct {
	OrderNo       string              `json:"order_no"`       // 商户支付号
	TotalCents    int64               `json:"total_cents"`    // 应付，分
	PaidCents     int64               `json:"paid_cents"`     // 实付，分
	RefundedCents int64               `json:"refunded_cents"` // 成功退款，分
	Status        money.PaymentStatus `json:"status"`         // 支付状态
}

// DetailFunds 保存支付摘要和退款列表，只供查询展示。
type DetailFunds struct {
	Payment *PaymentBrief    // 无支付记录为空
	Refunds []map[string]any // 退款查询结果；无记录为空数组
}

// UserOrderView 保留公开字段，确认金额缺失时省略，确认零值则显式输出。
type UserOrderView struct {
	OrderNo                  string                  `json:"order_no"`                      // 业务单号
	Status                   orderdomain.OrderStatus `json:"status"`                        // 订单状态
	DeviceID                 string                  `json:"device_id"`                     // 设备编号
	PortNo                   uint8                   `json:"port_no"`                       // 物理端口
	StartedAt                *time.Time              `json:"started_at"`                    // 开始 UTC 时刻，可空
	EndedAt                  *time.Time              `json:"ended_at"`                      // 结束 UTC 时刻，可空
	ChargedKWh               any                     `json:"charged_kwh"`                   // 确认 DECIMAL 文本或未结束时观测数值；缺失为空
	ChargedSeconds           *int64                  `json:"charged_seconds"`               // 确认时长，秒，可空
	DiscountCents            int64                   `json:"discount_cents"`                // 优惠，分
	ElectricCents            *int64                  `json:"electric_cents,omitempty"`      // 确认电费，分
	ServiceCents             *int64                  `json:"service_cents,omitempty"`       // 确认服务费，分
	TotalCents               *int64                  `json:"total_cents,omitempty"`         // 确认总费，分
	Live                     *charging.LiveMeterView `json:"live,omitempty"`                // 快照的观测与预估费用，不能作为结算金额
	LiveUnavailable          string                  `json:"live_unavailable,omitempty"`    // 快照实时估算不可读取的原因，正常为空
	ShortfallCents           *int64                  `json:"shortfall_cents,omitempty"`     // 确认缺口，分
	Display                  *charging.Display       `json:"display,omitempty"`             // 冻结展示规则
	Offer                    any                     `json:"offer,omitempty"`               // 有冻结方案时套餐（包括显式null）；无方案省略
	Tariff                   any                     `json:"tariff,omitempty"`              // 显示资费时分时规则（包括显式null）
	TariffLines              *[]string               `json:"tariff_lines,omitempty"`        // 冻结计价规则资费文案，可为空数组
	RuleDescription          *string                 `json:"rule_description,omitempty"`    // 显示规则时备注，允许空串
	BillingStatus            string                  `json:"billing_status"`                // 计费任务状态
	CardOperations           []CardOperation         `json:"card_operations"`               // 刷卡操作数组
	StartConfirmationPending bool                    `json:"start_confirmation_pending"`    // 订单为 paid，正在等待启动确认
	BillingCutoff            BillingCutoff           `json:"billing_cutoff"`                // 首次截止
	Payment                  *PaymentBrief           `json:"payment,omitempty"`             // 详情支付信息；快照省略
	Refunds                  *[]map[string]any       `json:"refunds,omitempty"`             // 详情退款数组；快照省略
	PollContinue             *bool                   `json:"poll_continue,omitempty"`       // 快照paid、charging继续轮询
	NextPollAfterMS          *int                    `json:"next_poll_after_ms,omitempty"`  // 快照轮询间隔，毫秒
	TelemetryAvailable       *bool                   `json:"telemetry_available,omitempty"` // 快照是否有功率或电量观测
	ElapsedSeconds           *int64                  `json:"elapsed_seconds,omitempty"`     // 当前时间减启动时间的秒数，只供展示
	TelemetryAt              *time.Time              `json:"telemetry_at,omitempty"`        // 最近有效观测 UTC 时刻
	CurrentPowerW            *float64                `json:"current_power_w,omitempty"`     // 观测 W，可为零
	VoltageV                 *float64                `json:"voltage_v,omitempty"`           // 观测 V，可为零
	TemperatureC             *float64                `json:"temperature_c,omitempty"`       // 观测摄氏度，可为零
}

// now 返回 s 的本次受控 UTC 时刻，无存储或网络 I/O。
func (s UserService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// base 将已授权订单的确认费用与冻结方案组合为详情字段。
func (s UserService) base(ctx context.Context, row order.ChargeOrderRecord) (UserOrderView, error) {
	out := UserOrderView{OrderNo: row.OrderNo, Status: row.Status, DeviceID: row.DeviceID, PortNo: row.PortNo, DiscountCents: row.DiscountCents, StartConfirmationPending: row.Status == orderdomain.StatusPaid}
	if row.StartedAt.Valid {
		at := row.StartedAt.Time.UTC()
		out.StartedAt = &at
	}
	if row.EndedAt.Valid {
		at := row.EndedAt.Time.UTC()
		out.EndedAt = &at
	}
	if row.ChargedKWh.Valid {
		out.ChargedKWh = row.ChargedKWh.String
	}
	if row.ChargedSeconds.Valid {
		out.ChargedSeconds = &row.ChargedSeconds.Int64
	}
	fees, err := s.Fees.Fees(ctx, []uint64{row.ID})
	if err != nil {
		return out, &UserReadError{Stage: "fee", Cause: err}
	}
	if f, ok := fees[row.ID]; ok {
		out.ElectricCents = &f.ElectricCents
		out.ServiceCents = &f.ServiceCents
		out.TotalCents = &f.TotalCents
		out.ShortfallCents = &f.ShortfallCents
	}
	state, err := s.State.Frozen(ctx, row)
	if err != nil {
		return out, &UserReadError{Stage: "state", Cause: err}
	}
	out.BillingStatus = state.BillingStatus
	out.CardOperations = state.CardOperations
	if out.CardOperations == nil {
		out.CardOperations = []CardOperation{}
	}
	out.BillingCutoff = state.Cutoff
	var frozen struct {
		Rule  charging.Rule   `json:"rule"`  // 冻结规则
		Offer *charging.Offer `json:"offer"` // 冻结套餐，可空
	}
	if len(state.Snapshot) > 0 && json.Unmarshal(state.Snapshot, &frozen) == nil && frozen.Rule.Spec.Scheme != nil {
		scheme := frozen.Rule.Spec.Scheme
		out.Display = &scheme.Display
		out.Offer = frozen.Offer
		if frozen.Rule.Spec.Display.ShowTariff {
			out.Tariff = scheme.Amount
			lines := tariffLines(*scheme, frozen.Offer)
			out.TariffLines = &lines
		}
		if frozen.Rule.Spec.Display.ShowRule {
			out.RuleDescription = &scheme.Remark
		}
	}
	return out, nil
}

// Detail 先校验 userID 对 raw 的归属，再组合费用、冻结与支付退款摘要。
// 返回本用例查询结果或校验、缺行、阶段错误，不调用网关或执行资金写入。
func (s UserService) Detail(ctx context.Context, userID uint64, raw string) (UserOrderView, error) {
	row, err := s.Orders.Resolve(ctx, userID, raw)
	if err != nil {
		return UserOrderView{}, err
	}
	out, err := s.base(ctx, row)
	if err != nil {
		return out, err
	}
	funds, err := s.State.Funds(ctx, row)
	if err != nil {
		return out, err
	}
	out.Payment = funds.Payment
	refunds := funds.Refunds
	if refunds == nil {
		refunds = []map[string]any{}
	}
	out.Refunds = &refunds
	return out, nil
}

// Snapshot 先校验订单属于当前用户，再补充最近两分钟的端口观测与实时估价。
// 网关失败时仍返回已有订单信息，并设 telemetry_available=false；数据库错误不能伪装为空值。
// 经过时长只供展示，预估费用不覆盖结算金额；已结束订单不再查询遥测或估费。
func (s UserService) Snapshot(ctx context.Context, userID uint64, raw string) (UserOrderView, error) {
	row, err := s.Orders.Resolve(ctx, userID, raw)
	if err != nil {
		return UserOrderView{}, err
	}
	out, err := s.base(ctx, row)
	if err != nil {
		return out, err
	}
	poll := row.Status == orderdomain.StatusPaid || row.Status == orderdomain.StatusCharging
	delay := 5000
	available := false
	out.PollContinue = &poll
	out.NextPollAfterMS = &delay
	out.TelemetryAvailable = &available
	if row.Status != orderdomain.StatusCharging || !row.StartedAt.Valid {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.Live != nil {
		// 归属已核对，向估费用例传入该单的设备、端口和开始时刻；保留结算金额。
		rows := []order.OrderView{{OrderID: row.ID, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: row.Status, StartedAt: out.StartedAt}}
		s.Live.Fill(ctx, rows)
		out.Live, out.LiveUnavailable = rows[0].Live, rows[0].LiveUnavailable
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
	now := s.now()
	elapsed := max(int64(0), int64(now.Sub(row.StartedAt.Time).Seconds()))
	out.ElapsedSeconds = &elapsed
	from := now.Add(-2 * time.Minute)
	if row.StartedAt.Time.After(from) {
		from = row.StartedAt.Time.UTC()
	}
	if !from.Before(now) {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.Gateway == nil {
		return out, nil
	}
	data, err := s.Gateway.Telemetry(ctx, gatewaycontract.TelemetryRequest{DeviceID: row.DeviceID, PortNo: row.PortNo, From: from, To: now})
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, nil
	}
	for _, p := range data.Series {
		at, err := time.Parse(time.RFC3339Nano, p.TS)
		if err != nil || at.Before(from) || at.After(now) {
			continue
		}
		at = at.UTC()
		out.TelemetryAt = &at
		if p.PowerW != nil || p.MeterKWh != nil {
			available = true
		}
		if p.PowerW != nil {
			out.CurrentPowerW = p.PowerW
		}
		if p.VoltageV != nil {
			out.VoltageV = p.VoltageV
		}
		if p.TemperatureC != nil {
			out.TemperatureC = p.TemperatureC
		}
		if p.MeterKWh != nil {
			out.ChargedKWh = *p.MeterKWh
		}
	}
	return out, nil
}
