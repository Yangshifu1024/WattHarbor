// Package card 定义在线卡刷卡操作的类型与状态取值词汇，供卡用例、
// 中央服务契约与运营后台共用。刷卡校验、设备指令与结算仍由所属用例负责。
//
// 本包独立于 internal/card：服务间契约不得引用业务实现包，
// 而 contracts/central 需要直接承载这两个取值，故类型定义放在此处。
package card

// CardOperationKind 是 card_operation.kind 列的操作类型，区分启动充电与增加时长。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
//
// 注意与 protocol.CommandKind 的 "start" 同值但不同义：前者是卡操作记录的业务
// 类型，后者是设备协议命令码，二者不得互相赋值或比较。
type CardOperationKind string

const (
	// CardKindStart 表示刷卡启动一次充电。
	CardKindStart CardOperationKind = "start"
	// CardKindExtend 表示刷卡为进行中的充电增加时长。
	CardKindExtend CardOperationKind = "extend"
)

// Valid 判断是否为该列已定义的取值。
func (k CardOperationKind) Valid() bool {
	switch k {
	case CardKindStart, CardKindExtend:
		return true
	default:
		return false
	}
}

// CardOperationStatus 是 card_operation.status 列的执行状态，用于设备确认结果
// 的落库与回查。取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
type CardOperationStatus string

const (
	// CardStatusConfirming 表示操作已登记、等待设备确认。
	CardStatusConfirming CardOperationStatus = "confirming"
	// CardStatusConfirmed 表示设备已确认，操作生效。
	CardStatusConfirmed CardOperationStatus = "confirmed"
	// CardStatusFailed 表示操作未能生效。
	CardStatusFailed CardOperationStatus = "failed"
)

// Valid 判断是否为该列已定义的取值。
func (s CardOperationStatus) Valid() bool {
	switch s {
	case CardStatusConfirming, CardStatusConfirmed, CardStatusFailed:
		return true
	default:
		return false
	}
}
