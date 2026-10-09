package charging

import "errors"

// ChargeExecutor 指定服务端或设备负责计价。
type ChargeExecutor string

const (
	// ExecutorServer 由平台按计量结果计价，设备负责上报。
	ExecutorServer ChargeExecutor = "server"
	// ExecutorDevice 由设备执行购买额度，服务端按 SettleSession 计算应收和余款。
	ExecutorDevice ChargeExecutor = "device"
)

// ChargeMode 以独立枚举表示服务端和设备的计费模式。
type ChargeMode string

const (
	// 服务端模式：费用由 Cost 按真实用量算出来。
	ModeServerRealtimePower ChargeMode = "server_realtime_power" // 按实时功率
	ModeServerMaxPower      ChargeMode = "server_max_power"      // 按最大功率
	ModeServerEnergy        ChargeMode = "server_energy"         // 按电量
	// 设备模式按购买额度执行；提前结束的费用由 SettleSession 计算。
	ModeDeviceDuration ChargeMode = "device_duration" // 时长
	ModeDeviceEnergy   ChargeMode = "device_energy"   // 电量
	ModeDevicePower    ChargeMode = "device_power"    // 功率档位
)

var (
	// ErrNotServerBilled 表示当前模式由设备计费，不支持服务端费率计算。
	ErrNotServerBilled = errors.New("该计费方式由设备执行，服务端不计算费用")
)

// Executor 返回计费模式所属的执行方。
func (m ChargeMode) Executor() ChargeExecutor {
	switch m {
	case ModeServerRealtimePower, ModeServerMaxPower, ModeServerEnergy:
		return ExecutorServer
	case ModeDeviceDuration, ModeDeviceEnergy, ModeDevicePower:
		return ExecutorDevice
	default:
		return ""
	}
}

// ServerBilled 判断该模式是否由 Cost 计算最终费用。
func (m ChargeMode) ServerBilled() bool { return m.Executor() == ExecutorServer }

// IsDevice 判断执行方是否为设备。
func (e ChargeExecutor) IsDevice() bool { return e == ExecutorDevice }

// Valid 判断当前版本是否支持该计费模式。
func (m ChargeMode) Valid() bool { return m.Executor() != "" }

// ServerBasis 指定服务端电费算法。
type ServerBasis string

const (
	// BasisRealtimePower 按各分段功率选档，以小时单价计算费用。
	BasisRealtimePower ServerBasis = "realtime_power"
	// BasisMaxPower 按每个费率时段的峰值功率选择该时段档位。
	BasisMaxPower ServerBasis = "max_power"
	// BasisEnergy 按各时段的电量与每 kWh 单价计费。
	BasisEnergy ServerBasis = "energy"
)

// BasisFor 返回服务端模式对应的电价算法，避免模式与费率配置不一致。
func (m ChargeMode) BasisFor() ServerBasis {
	switch m {
	case ModeServerRealtimePower:
		return BasisRealtimePower
	case ModeServerMaxPower:
		return BasisMaxPower
	case ModeServerEnergy:
		return BasisEnergy
	default:
		return ""
	}
}

// ServiceBasis 保存旧版独立服务费算法；当前 Cost 从电费时段或功率档位读取服务费。
type ServiceBasis string

const (
	// ServiceNone 标记旧版不收服务费配置。
	ServiceNone ServiceBasis = "none"
	// ServiceEnergy 标记旧版每 kWh 服务费。
	ServiceEnergy ServiceBasis = "energy"
	// ServiceMinutePower 是旧版按功率阶梯选择服务费单价的标记，当前 Cost 不使用。
	ServiceMinutePower ServiceBasis = "minute_power"
	// ServiceMinute 标记旧版每分钟服务费。
	ServiceMinute ServiceBasis = "minute"
	// ServiceSession 标记旧版每次充电服务费。
	ServiceSession ServiceBasis = "session"
)

// Valid 判断是否为已定义的旧版服务费算法。
func (b ServiceBasis) Valid() bool {
	switch b {
	case ServiceNone, ServiceEnergy, ServiceMinutePower, ServiceMinute, ServiceSession:
		return true
	default:
		return false
	}
}

// TierPriceBasis 保留旧版阶梯价格单位；当前 Cost 使用档位的分/小时价格，新模板禁止该字段。
type TierPriceBasis string

const (
	// TierPerHourAtCeiling 标记旧版按功率上限换算的价格，当前计算不使用。
	TierPerHourAtCeiling TierPriceBasis = "per_hour_at_ceiling"
	// TierPerKWh 标记旧版每 kWh 单价，当前计算不使用。
	TierPerKWh TierPriceBasis = "per_kwh"
)
