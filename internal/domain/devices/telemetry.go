// Package devices 定义设备领域的只读事实类型：端口、读数、遥测。
//
// 这些类型与厂商协议无关。各协议适配器把自己的帧翻译成这里的读数，
// 领域层只消费 Reading，不知道 0xB6 是什么、也不知道心跳的帧长。
package devices

import "time"

// Reading 是一份计量证据：某个端口在某个时刻的累计读数与瞬时功率。
//
// 累计量而非瞬时值是关键：设备侧只有累计电表，计费按相邻读数的差值
// 分段计算，不从电量反推功率。PowerW 为 nil 表示该时刻没有功率读数，
// 不得用其他区间的值顶替。
type Reading struct {
	// Port 端口号，从 1 开始。
	Port uint8
	// At 该读数在设备时钟上的时刻，计费以此定位，不使用接收时间。
	At time.Time
	// ChargedSeconds 设备侧累计充电秒数。分段时先归一到充电开始的时刻，
	// 再由 delta 推出每个读数的绝对时间。
	ChargedSeconds uint32
	// ChargedMWh 累计电量，单位 mWh。必须能被 1000 整除才能换算成 Wh，
	// 整除不成立说明设备上报了不可用的精度。
	ChargedMWh uint32
	// PowerW 瞬时功率，单位 W。设备以 0.1 W 为单位上报，此处已换算；
	// 设备上报的原始值经换算后为 0 时也记 0，不记为缺测。
	PowerW uint32
}

// ReadingSample 是一次上报携带的一组读数，ReceivedAt 是服务端收到的时间。
//
// ReceivedAt 只用于剔除过期或前后矛盾的上报，绝不用于分摊电量。
// 与之相对 Reading.At 是设备时钟上的时刻，是计量的唯一时间基准。
type ReadingSample struct {
	DeviceID   string
	ReceivedAt time.Time
	Readings   []Reading
}

// SessionSpan 是一次充电过程在设备侧的起止与总量，作为分段计费的收口证据。
type SessionSpan struct {
	DeviceID  string
	Port      uint8
	StartedAt time.Time
	EndedAt   time.Time
	// ChargedSeconds 设备侧累计秒数。与 EndedAt-StartedAt 不一致说明
	// 证据自相矛盾，计费必须拒绝而不是取其一。
	ChargedSeconds uint32
	// EnergyMilliKWh 设备侧累计电量，单位 mWh。作为分段求和的校验值。
	EnergyMilliKWh uint32
}
