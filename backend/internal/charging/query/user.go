package query

import (
	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/order"
	"context"
	"encoding/json"
	"time"
)

// UserOrders 只提供按用户隔离的原订单，不允许调用方传入未经授权的数据库记录。
type UserOrders interface {
	// Resolve 在 ctx 下核对 userID 对 raw 原业务号的归属，返回原订单或输入／缺失／存储错误。
	Resolve(ctx context.Context, userID uint64, raw string) (order.ChargeOrderRecord, error)
}

// UserStateReader 提供原冻结与资金读模型，不提供任意表名或可继续拼接的查询。
type UserStateReader interface {
	// Frozen 在 ctx 下组合已授权 row 的冻结事实，返回状态或存储错误，无写入。
	Frozen(ctx context.Context, row order.ChargeOrderRecord) (FrozenState, error)
	// Funds 在 ctx 下组合已授权 row 的支付／退款事实，返回状态或分阶段存储错误，无资金写入。
	Funds(ctx context.Context, row order.ChargeOrderRecord) (DetailFunds, error)
}

// UserCurveReader 提供原设备端口的有界观测，不持有业务授权或改变订单状态。
type UserCurveReader interface {
	// Telemetry 在 ctx 下读取 q 原设备窗口，返回共享契约或网络／契约错误，无写入。
	Telemetry(ctx context.Context, q gatewaycontract.TelemetryRequest) (gatewaycontract.TelemetryReply, error)
	// HistoricalCurve 在 ctx 下读取 q 原订单聚合窗口，返回共享契约或网络／契约错误，无写入。
	HistoricalCurve(ctx context.Context, q gatewaycontract.HistoricalCurveRequest) (gatewaycontract.HistoricalCurveReply, error)
}

// UserLiveReader 提供已授权订单的实时估算，只填充公开读模型，不写入结算费用。
type UserLiveReader interface {
	// Fill 在 ctx 下为 rows 中充电订单补充观测与预估费用，失败保留不可读取提示。
	Fill(ctx context.Context, rows []order.OrderView)
}

// UserService 拥有用户充电详情、状态补充和曲线窗口；确认金额、实时估价与遥测展示保持独立。
type UserService struct {
	Orders         UserOrders       // 所属订单隔离读取
	Fees           order.FeeReader  // 所属结算确认费用，不计算实时估价
	State          UserStateReader  // 冻结方案、计费进度、截止及原资金摘要
	Gateway        UserCurveReader  // cmd 注入的具名网关读取
	Live           UserLiveReader   // cmd 注入与后台同口径的实时估算
	AllowedWindows []string         // 允许的既定回看窗口；空值使用三个原默认窗口
	Now            func() time.Time // 可选受控时钟；空值使用当前 UTC 时刻
}

// UserReadError 标记组合查询阶段，供 HTTP 映射原文案；Cause 保留取消／缺行事实。
type UserReadError struct {
	Stage string // fee、state、payment、refund、curve或history_curve
	Cause error  // 原所属读取错误
}

// Error 返回 e 的原原因，无 IO。
func (e *UserReadError) Error() string { return e.Cause.Error() }

// Unwrap 返回 e 的原原因，供取消及缺行判断，无副作用。
func (e *UserReadError) Unwrap() error { return e.Cause }

// UserInputError 是既定窗口准入结果，不包含他人订单事实。
type UserInputError struct {
	Kind    string // input非法窗口、conflict未开始／倒置窗口、expired超过保留期
	Message string // 原公开错误文案
}

// Error 返回 e 的公开文案，无 IO。
func (e *UserInputError) Error() string { return e.Message }

// CardOperation 是本用例的原刷卡读模型，不携带 ORM 映射。
type CardOperation struct {
	OperationID   string `json:"operation_id"`    // 原操作 UUID
	DeviceID      string `json:"device_id"`       // 原设备身份
	EventID       string `json:"event_id"`        // 原设备事件身份
	ChargeOrderID uint64 `json:"charge_order_id"` // 原充电订单主键
	CardID        uint64 `json:"card_id"`         // 原卡主键
	Kind          string `json:"kind"`            // start或extend
	PriceCents    int64  `json:"price_cents"`     // 原扣款分值
	Minutes       uint16 `json:"minutes"`         // 原购买分钟数
	Status        string `json:"status"`          // 原确认状态
}

// BillingCutoff 保留原截止文本与未冻结空值，不解析为新的资金时刻。
type BillingCutoff struct {
	CutoffAt *string `json:"cutoff_at"` // 首次截止文本；无记录为空
	Reason   string  `json:"reason"`    // 原截止原因；无记录为空串
}

// FrozenState 是所属模块只读组合，未保存快照时 Snapshot 为空。
type FrozenState struct {
	Snapshot       []byte          // 原冻结 JSON，不读取当前模板
	BillingStatus  string          // 原计费任务状态，无任务为空串
	CardOperations []CardOperation // 原有序操作，无操作为空数组
	Cutoff         BillingCutoff   // 原截止记录，无记录保持空值
}

// PaymentBrief 保留原已确认支付金额，不携带 ORM 映射。
type PaymentBrief struct {
	OrderNo       string `json:"order_no"`       // 原商户支付号
	TotalCents    int64  `json:"total_cents"`    // 原应付，分
	PaidCents     int64  `json:"paid_cents"`     // 原实付，分
	RefundedCents int64  `json:"refunded_cents"` // 原成功退款，分
	Status        string `json:"status"`         // 原支付状态
}

// DetailFunds 是原支付／退款事实，不提供资金执行能力。
type DetailFunds struct {
	Payment *PaymentBrief    // 无支付记录为空
	Refunds []map[string]any // 原退款投影；无记录为空数组
}

// UserOrderView 保留原公开字段，确认金额缺失时省略，确认零值则显式输出。
type UserOrderView struct {
	OrderNo                  string                  `json:"order_no"`                      // 原业务单号
	Status                   string                  `json:"status"`                        // 原订单状态
	DeviceID                 string                  `json:"device_id"`                     // 原设备身份
	PortNo                   uint8                   `json:"port_no"`                       // 原物理端口
	StartedAt                *time.Time              `json:"started_at"`                    // 原开始 UTC 时刻，可空
	EndedAt                  *time.Time              `json:"ended_at"`                      // 原结束 UTC 时刻，可空
	ChargedKWh               any                     `json:"charged_kwh"`                   // 原确认 DECIMAL 文本或未结束时观测数值；缺失为空
	ChargedSeconds           *int64                  `json:"charged_seconds"`               // 原确认时长，秒，可空
	DiscountCents            int64                   `json:"discount_cents"`                // 原优惠，分
	ElectricCents            *int64                  `json:"electric_cents,omitempty"`      // 原确认电费，分
	ServiceCents             *int64                  `json:"service_cents,omitempty"`       // 原确认服务费，分
	TotalCents               *int64                  `json:"total_cents,omitempty"`         // 原确认总费，分
	Live                     *charging.LiveMeterView `json:"live,omitempty"`                // 快照的观测与预估费用，不能作为结算金额
	LiveUnavailable          string                  `json:"live_unavailable,omitempty"`    // 快照实时估算不可读取的原因，正常为空
	ShortfallCents           *int64                  `json:"shortfall_cents,omitempty"`     // 原确认缺口，分
	Display                  *charging.Display       `json:"display,omitempty"`             // 原冻结展示规则
	Offer                    any                     `json:"offer,omitempty"`               // 有冻结方案时原套餐（包括显式null）；无方案省略
	Tariff                   any                     `json:"tariff,omitempty"`              // 显示资费时原分时规则（包括显式null）
	TariffLines              *[]string               `json:"tariff_lines,omitempty"`        // 冻结口径资费文案，可为空数组
	RuleDescription          *string                 `json:"rule_description,omitempty"`    // 显示规则时原备注，允许空串
	BillingStatus            string                  `json:"billing_status"`                // 原计费任务状态
	CardOperations           []CardOperation         `json:"card_operations"`               // 原刷卡操作数组
	StartConfirmationPending bool                    `json:"start_confirmation_pending"`    // 原paid待启动事实
	BillingCutoff            BillingCutoff           `json:"billing_cutoff"`                // 原首次截止
	Payment                  *PaymentBrief           `json:"payment,omitempty"`             // 详情支付事实；快照省略
	Refunds                  *[]map[string]any       `json:"refunds,omitempty"`             // 详情原退款数组；快照省略
	PollContinue             *bool                   `json:"poll_continue,omitempty"`       // 快照paid／charging继续轮询
	NextPollAfterMS          *int                    `json:"next_poll_after_ms,omitempty"`  // 原快照轮询间隔，毫秒
	TelemetryAvailable       *bool                   `json:"telemetry_available,omitempty"` // 快照是否有功率或电量观测
	ElapsedSeconds           *int64                  `json:"elapsed_seconds,omitempty"`     // 展示墙钟秒数，不进入计费
	TelemetryAt              *time.Time              `json:"telemetry_at,omitempty"`        // 最近有效观测 UTC 时刻
	CurrentPowerW            *float64                `json:"current_power_w,omitempty"`     // 原观测 W，可为零
	VoltageV                 *float64                `json:"voltage_v,omitempty"`           // 原观测 V，可为零
	TemperatureC             *float64                `json:"temperature_c,omitempty"`       // 原观测摄氏度，可为零
}

// now 返回 s 的本次受控 UTC 时刻，无存储或网络 IO。
func (s UserService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// base 在 ctx 下组合已授权 row 的原确认费用和冻结状态，返回原读模型或阶段错误，无写入。
func (s UserService) base(ctx context.Context, row order.ChargeOrderRecord) (UserOrderView, error) {
	out := UserOrderView{OrderNo: row.OrderNo, Status: row.Status, DeviceID: row.DeviceID, PortNo: row.PortNo, DiscountCents: row.DiscountCents, StartConfirmationPending: row.Status == "paid"}
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
		Rule  charging.Rule   `json:"rule"`  // 原冻结规则
		Offer *charging.Offer `json:"offer"` // 原冻结套餐，可空
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

// Detail 在 ctx 下先校验 userID 对 raw 的归属，再组合原费用、冻结与支付退款摘要。
// 返回本用例读模型或准入／缺行／阶段错误，不调用网关或执行资金写入。
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

// Snapshot 在 ctx 下先校验 userID 对 raw 的归属，再补充最近两分钟的原端口观测。
// 网关失败保持原快照成功且telemetry_available=false；所属存储失败不伪造空状态。
// 墙钟秒数仅展示，live复用后台实时估价且不覆盖确认费用，已结束订单不读取遥测或估价。
func (s UserService) Snapshot(ctx context.Context, userID uint64, raw string) (UserOrderView, error) {
	row, err := s.Orders.Resolve(ctx, userID, raw)
	if err != nil {
		return UserOrderView{}, err
	}
	out, err := s.base(ctx, row)
	if err != nil {
		return out, err
	}
	poll := row.Status == "paid" || row.Status == "charging"
	delay := 5000
	available := false
	out.PollContinue = &poll
	out.NextPollAfterMS = &delay
	out.TelemetryAvailable = &available
	if row.Status != "charging" || !row.StartedAt.Valid {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.Live != nil {
		// 先完成用户归属校验，再向既有估价用例传入该单身份；确认金额字段保持原值。
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
