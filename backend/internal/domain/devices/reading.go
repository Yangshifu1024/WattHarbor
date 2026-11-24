// Package devices 定义端口与计量读数类型，不依赖厂商协议；协议帧由适配器转换。
package devices

import "time"

// Reading 保存某端口的累计电量与瞬时功率，计费按相邻累计读数的差值分段。
// 功率零值表示设备上报 0 W，不能当作缺测，也不能从电量反推功率。
type Reading struct {
	// Port 端口号，从 1 开始。
	Port uint8
	// At 保存读数时间；当前分段算法用会话起点加累计秒数定位，不依赖此字段。
	At time.Time
	// ChargedSeconds 是设备累计充电秒数，加上设备会话起点得到读数时刻。
	ChargedSeconds uint32
	// ChargedMWh 累计电量，单位 mWh。必须能被 1000 整除才能换算成 Wh。
	// 整除不成立说明设备上报了不可用的精度。
	ChargedMWh uint32
	// PowerW 瞬时功率，单位 W。设备以 0.1 W 为单位上报，此处已换算。
	// 设备上报的原始值经换算后为 0 时也记 0，不记为缺测。
	PowerW uint32
}

// ReadingSample 保存一组设备读数及服务端接收时间。
// ReceivedAt 用于检查上报延迟与时钟偏差，不用于分摊电量。
type ReadingSample struct {
	DeviceID   string
	ReceivedAt time.Time
	Readings   []Reading
}

// SessionSpan 保存设备上报的开始、结束、总时长和总电量，用于校验分段合计。
type SessionSpan struct {
	DeviceID  string
	Port      uint8
	StartedAt time.Time
	EndedAt   time.Time
	// ChargedSeconds 是设备累计秒数；必须等于 EndedAt 与 StartedAt 的时间差。
	ChargedSeconds uint32
	// EnergyWh 是设备累计电量，单位 Wh，用于校验分段合计。
	// 来源 protocol.Event.EnergyMilliKWh 虽含 MilliKWh，实际保存 Wh。
	EnergyWh uint32
}
