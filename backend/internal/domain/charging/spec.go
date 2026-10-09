// Package charging 定义充电方案、购买额度和计量规则，计算费用及停止条件。
package charging

import (
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

var (
	// ErrInvalidPricing 表示规则、价格或用量参数不符合计算要求。
	ErrInvalidPricing = errors.New("invalid quote input or pricing rule")
	// ErrMeterReview 表示读数缺失、分段不连续或合计不符，须人工核对计量。
	ErrMeterReview = errors.New("实际计量不足或矛盾，需补充分时计量后核算")
	// ErrRuleUnavailable 表示站点没有可用的已发布计费规则。
	ErrRuleUnavailable = errors.New("active station pricing rule unavailable")
	// ErrLegacyPricing 表示模板包含旧版实时功率价格，须先转换价格单位。
	ErrLegacyPricing = errors.New("实时功率模板使用旧版电价，请先编辑并转换为元/度后再保存、复制或应用")

	beijing = time.FixedZone("Asia/Shanghai", 8*3600)
)

const maxRateCents = 1000000

// Tier 表示功率阶梯的一个档位，仅保存上界；下界由上一档上界加 1 W 推导。
// 每档从上一档上界加 1 W 开始，第一档从 0 W 开始。
type Tier struct {
	// MaxWatts 是含端点的上界。第一档下限为 0，各档上界必须严格递增。
	MaxWatts int `json:"max_watts"`
	// ElectricCents 在两种功率算法中均为每小时的分。
	ElectricCents int64 `json:"electric_cents"`
	// ServiceCents 是同一档位的每小时服务费。
	ServiceCents int64 `json:"service_cents,omitempty"`
}

// Period 保存一天内一个费率时段；开始分钟取上一时段的结束分钟，第一段从零点开始。
type Period struct {
	ServiceCents int64 `json:"service_cents,omitempty"`
	// EndMinute 是从零点起的结束分钟，取值 1..1440；必须严格递增，最后一项为 1440。
	EndMinute int `json:"end_minute"`
	// ElectricCents 是电量计费的单一电价，单位分/kWh；该计价方式不使用阶梯。
	ElectricCents int64 `json:"electric_cents,omitempty"`
	// Tiers 是该时段内适用的功率阶梯；电量计价方式下为空。
	Tiers []Tier `json:"tiers,omitempty"`
}

// ElectricLine 表示服务端电价配置；设备计费模式不配置服务端电价。
type ElectricLine struct {
	Basis   ServerBasis `json:"basis"`
	Periods []Period    `json:"periods"`
}

// ServiceLine 保存旧版独立服务费配置；当前 Cost 从 Period 或 Tier 读取服务费。
type ServiceLine struct {
	Basis           ServiceBasis `json:"basis"`
	CentsPerKWh     int64        `json:"cents_per_kwh,omitempty"`
	CentsPerMinute  int64        `json:"cents_per_minute,omitempty"`
	CentsPerSession int64        `json:"cents_per_session,omitempty"`
}

// Channel 标记启动入口；当前 Cost 不按此字段调整费用。
type Channel string

const (
	// ChannelDefault 表示未区分扫码或刷卡的入口。
	ChannelDefault Channel = "default"
	// ChannelTemp 表示扫码临时充电入口。
	ChannelTemp Channel = "temp"
	// ChannelCard 表示刷卡入口。
	ChannelCard Channel = "card"
)

// ChannelMultiplier 保存旧版电费渠道倍率，10000 表示 1 倍；当前 Cost 不使用。
type ChannelMultiplier struct {
	TempBP int32 `json:"temp_bp"`
	CardBP int32 `json:"card_bp"`
}

func (m *ChannelMultiplier) electricBP(channel Channel) int32 {
	if m == nil {
		return 10000
	}
	if channel == ChannelTemp && m.TempBP > 0 {
		return m.TempBP
	}
	if channel == ChannelCard && m.CardBP > 0 {
		return m.CardBP
	}
	return 10000
}

// TimeCharge 保存按时长充电的停止条件，不定义费率。
type TimeCharge struct {
	// StopWhenFull 启用设备充满自停，不等待时长耗尽。
	StopWhenFull bool `json:"stop_when_full"`
	// MaxMinutes 是最长充电分钟数，0 表示不限。
	MaxMinutes uint16 `json:"max_minutes,omitempty"`
	// FloatPowerDeciWatts 与 FloatSeconds 保存设备浮充阈值（0.1 W）和持续秒数。
	FloatPowerDeciWatts uint16 `json:"float_power_deci_watts,omitempty"`
	FloatSeconds        uint16 `json:"float_seconds,omitempty"`
}

// Spec 包含完整计价配置，预估与结算共用此结构和 Cost 引擎。
type Spec struct {
	Scheme *Scheme    `json:"scheme,omitempty"`
	Mode   ChargeMode `json:"mode"`
	// Electric 仅用于服务端计费；设备计费必须为零值，ValidateSpec 拒绝无效费率配置。
	Electric *ElectricLine `json:"electric,omitempty"`
	// Service 保留旧版独立服务费配置；当前 Cost 不使用，新模板必须为空。
	Service *ServiceLine `json:"service,omitempty"`
	// Multiplier 保留旧版渠道倍率；当前 Cost 不使用，新模板必须为空。
	Multiplier *ChannelMultiplier `json:"multiplier,omitempty"`
	// TierPriceBasis 保留旧版阶梯价格单位标记；当前 Cost 不使用，新模板必须为空。
	TierPriceBasis TierPriceBasis `json:"tier_price_basis,omitempty"`
	// LossRateBP 保留旧版损耗系数（基点）；当前 Cost 不使用，新模板必须为 0。
	LossRateBP int32 `json:"loss_rate_bp,omitempty"`
	// FreeMinutes 表示充电在这么多分钟内结束时整单免单，0 表示不启用该免单。
	FreeMinutes int `json:"free_minutes,omitempty"`
	// MinElectricCents 为电费最低收费，单位为分，不调整服务费。
	MinElectricCents int64 `json:"min_electric_cents,omitempty"`
	// TimeCharge 保存停止条件；DecideStop 使用其中的 MaxMinutes 判断最长时长。
	TimeCharge *TimeCharge `json:"time_charge,omitempty"`
	// SpendCapCents 是服务端计费的费用上限，单位为分；0 表示未配置。
	// 未配置时仍保留设备下发的时间或电量额度限制。
	SpendCapCents int64 `json:"spend_cap_cents,omitempty"`
	// StopGraceSeconds 是费用封顶停机计划附带的等待秒数；本包不判断设备是否已停。
	StopGraceSeconds int `json:"stop_grace_seconds,omitempty"`
	// DefaultChargeWay 是本设备上充电默认的鉴权方式，属于展示与策略，不参与计算。
	DefaultChargeWay string `json:"default_charge_way,omitempty"`
	// CardMaxMinutes 是允许的刷卡充电最长时长。
	CardMaxMinutes uint16 `json:"card_max_minutes,omitempty"`
	// Display 控制用户端呈现，与实际计费参数独立。
	Display Display `json:"display"`
}

// Sample 保存连续区间的用量。电量计费要求区间内费率不变，功率计费由 Cost 按时段切分。
type Sample struct {
	PowerKnown bool
	Start      time.Time
	End        time.Time
	EnergyWh   uint64
	// PowerW 是区间功率，单位 W；为 0 且 PowerKnown=false 时不能用于功率计费。
	PowerW uint32
}

// Usage 是 Cost 的用量输入；时间跨度最多 7 天，电量单位为 Wh。
type Usage struct {
	Start    time.Time
	End      time.Time
	EnergyWh uint64
	Samples  []Sample
	Channel  Channel
}

func (u Usage) channel(spec Spec) Channel {
	if u.Channel != "" {
		return u.Channel
	}
	return ChannelDefault
}

func (u Usage) minutes() int64 {
	if u.Start.IsZero() || u.End.IsZero() || !u.End.After(u.Start) {
		return 0
	}
	return int64(u.End.Sub(u.Start) / time.Minute)
}

// Fee 保存计价结果，Basis 保留计价规则供展示单位。
type Fee struct {
	Fragments     []FeeFragment `json:"fragments,omitempty"`
	Basis         ServerBasis   `json:"basis"`
	ElectricCents int64         `json:"electric_cents"`
	ServiceCents  int64         `json:"service_cents"`
	TotalCents    int64         `json:"total_cents"`
	BillableWh    uint64        `json:"billable_wh"`
}

// ValidateSpec 共用于发布和计费，确保已发布规则满足结算执行条件。
func ValidateSpec(spec Spec) error {
	if !spec.Mode.Valid() {
		return ErrInvalidPricing
	}
	if spec.FreeMinutes < 0 || spec.FreeMinutes > 1440 {
		return ErrInvalidPricing
	}
	if spec.MinElectricCents < 0 || spec.MinElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.SpendCapCents < 0 || spec.SpendCapCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.StopGraceSeconds < 0 || spec.StopGraceSeconds > 3600 {
		return ErrInvalidPricing
	}
	if spec.LossRateBP < 0 || spec.LossRateBP > 100000 {
		return ErrInvalidPricing
	}
	if spec.Multiplier != nil {
		for _, bp := range []int32{spec.Multiplier.TempBP, spec.Multiplier.CardBP} {
			if bp < 0 || bp > 100000 {
				return ErrInvalidPricing
			}
		}
	}
	if spec.CardMaxMinutes > 4320 {
		// 刷卡时长使用 uint16 分钟字段，超出范围时拒绝，不截断数值。
		return ErrInvalidPricing
	}
	if spec.TimeCharge != nil {
		if spec.TimeCharge.MaxMinutes > 4320 || spec.TimeCharge.FloatSeconds > 10800 || spec.TimeCharge.FloatPowerDeciWatts > 500 {
			return ErrInvalidPricing
		}
	}
	if spec.Service != nil {
		if !spec.Service.Basis.Valid() {
			return ErrInvalidPricing
		}
		for _, amount := range []int64{spec.Service.CentsPerKWh, spec.Service.CentsPerMinute, spec.Service.CentsPerSession} {
			if amount < 0 || amount > maxRateCents {
				return ErrInvalidPricing
			}
		}
	}
	if !spec.Mode.ServerBilled() {
		// 设备计费不使用服务端费率，拒绝携带无效费率的规则。
		if spec.Electric != nil || spec.Service != nil || spec.Multiplier != nil {
			return ErrInvalidPricing
		}
		return nil
	}
	if spec.Electric == nil {
		return ErrInvalidPricing
	}
	if spec.Electric.Basis != spec.Mode.BasisFor() {
		// 计费模式必须与电价算法一致。
		return ErrInvalidPricing
	}
	for _, period := range spec.Electric.Periods {
		if err := validateTiers(spec.Electric.Basis, period); err != nil {
			return err
		}
	}
	_, err := compilePeriods(spec.Electric.Periods)
	return err
}

// ValidateTemplateSpec 校验新写入或新发布的模板。结算仍用 ValidateSpec。
// 避免把旧订单的冻结价格拒掉或按新算法重算。
func ValidateTemplateSpec(spec Spec) error {
	if spec.Scheme != nil {
		return spec.Scheme.Validate()
	}
	if err := ValidateSpec(spec); err != nil {
		return err
	}
	if spec.Service != nil || spec.SpendCapCents != 0 || spec.TierPriceBasis != "" || spec.LossRateBP != 0 || spec.Multiplier != nil {
		return ErrInvalidPricing
	}
	return nil
}

// compilePeriods 把全天时段展开为按分钟索引的费率表。
// 结束分钟必须严格递增并覆盖 [0, 1440)，保证全天无空档或重叠。
func compilePeriods(periods []Period) ([1440]Period, error) {
	var schedule [1440]Period
	if len(periods) == 0 || len(periods) > 48 {
		return schedule, ErrInvalidPricing
	}
	cursor := 0
	for _, period := range periods {
		if period.EndMinute <= cursor || period.EndMinute > 1440 {
			return schedule, ErrInvalidPricing
		}
		for minute := cursor; minute < period.EndMinute; minute++ {
			schedule[minute] = period
		}
		cursor = period.EndMinute
	}
	if cursor != 1440 {
		return schedule, ErrInvalidPricing
	}
	return schedule, nil
}

// validateTiers 校验功率阶梯连续覆盖；电量单价与功率阶梯不能同时配置。
func validateTiers(basis ServerBasis, period Period) error {
	if period.ServiceCents < 0 || period.ServiceCents > maxRateCents {
		return ErrInvalidPricing
	}
	if period.ElectricCents < 0 || period.ElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if basis == BasisEnergy {
		if len(period.Tiers) > 0 {
			return ErrInvalidPricing
		}
		return nil
	}
	if period.ElectricCents != 0 || len(period.Tiers) == 0 || len(period.Tiers) > 8 {
		return ErrInvalidPricing
	}
	previous := -1
	for _, tier := range period.Tiers {
		if tier.MaxWatts < 0 || tier.MaxWatts <= previous || tier.MaxWatts > 9990 {
			return ErrInvalidPricing
		}
		if tier.ElectricCents < 0 || tier.ElectricCents > maxRateCents ||
			tier.ServiceCents < 0 || tier.ServiceCents > maxRateCents {
			return ErrInvalidPricing
		}
		previous = tier.MaxWatts
	}
	return nil
}

func clockMinute(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, ErrInvalidPricing
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, ErrInvalidPricing
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil || hour > 24 || minute > 59 || (hour == 24 && minute != 0) {
		return 0, ErrInvalidPricing
	}
	return hour*60 + minute, nil
}

// specIsUniformOver 比较区间内的费率；电量费率相同时可直接使用区间总电量。
func specIsUniformOver(spec Spec, start, end time.Time) bool {
	if spec.Electric == nil || (spec.Electric.Basis != BasisEnergy && spec.Electric.Basis != BasisRealtimePower) {
		// 此检查只比较电量和实时功率费率；其他模式直接返回 true。
		return true
	}
	schedule, err := compilePeriods(spec.Electric.Periods)
	if err != nil {
		return false
	}
	var first *Period
	at := start.In(beijing).Truncate(time.Minute)
	for !at.After(end.In(beijing).Truncate(time.Minute)) {
		period := schedule[at.Hour()*60+at.Minute()]
		if first == nil {
			first = &period
		} else if !sameRate(*first, period) {
			return false
		}
		at = at.Add(time.Minute)
	}
	return true
}

// sameRate 比较单一电价和阶梯，判断两个时段费率是否完全一致。
// 仅比较阶梯会误将不同的电量电价视为相同，导致跨费率电量错误分摊。
func sameRate(a, b Period) bool {
	if a.ElectricCents != b.ElectricCents || a.ServiceCents != b.ServiceCents || len(a.Tiers) != len(b.Tiers) {
		return false
	}
	for i := range a.Tiers {
		if a.Tiers[i] != b.Tiers[i] {
			return false
		}
	}
	return true
}

var energyPattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3})?$`)

var (
	decimalZero     = decimal.Zero
	decimalOne      = decimal.NewFromInt(1)
	decimalThousand = decimal.NewFromInt(1000)
)

// parseEnergy 限制用户输入的电量范围，拒绝格式错误或过大的数值。
func parseEnergy(value string) (decimal.Decimal, error) {
	kwh, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, ErrInvalidPricing
	}
	if kwh.LessThan(decimal.New(1, -3)) || kwh.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, ErrInvalidPricing
	}
	return kwh, nil
}
