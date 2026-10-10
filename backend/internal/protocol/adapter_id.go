package protocol

// AdapterID 标识一种设备协议适配器，出现在三个 VARCHAR 列与协议帧字段：
// device_meta.protocol_adapter、vendor.adapter_class 与 device_event.protocol_name。
// 数据库不对该集合加约束，词表以本类型为准。
//
// 当前受支持的集合只有 DC589；其余厂商协议的适配器登记后在此追加常量。
// Known() 只是识别辅助，不是准入门槛：写入路径（厂商资料、CSV 导入）接受
// 尚未登记的标识并原样落库，读取路径也不得据此拒绝。
//
// 不得与下列词表混用：
//   - Transport（tcp／mqtt）：连接传输方式，见 session_audit.go
//   - EventType（heartbeat／fault 等）：上报事件种类，见 adapter.go
//
// 传输方式与事件类型描述的是一次连接或一次上报的某个维度，
// 而 AdapterID 描述的是「这台设备说的是哪种协议」，三者相互独立。
type AdapterID string

// AdapterIDDC589 是当前唯一受支持的设备协议适配器。
const AdapterIDDC589 AdapterID = "dc589"

// Known 判断是否为已登记的适配器。
// 仅用于识别；不得据此拒绝写入或替换读取值，未知标识按原值透传。
func (a AdapterID) Known() bool {
	return a == AdapterIDDC589
}
