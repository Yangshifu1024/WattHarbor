package card

import (
	"context"

	"gorm.io/gorm"
)

// 刷卡相关的表模型：在线卡、刷卡会话与刷卡操作记录。
// 这些原先散在 database 的 money_schema.go / admin_card_alert.go /
// order_authorization.go 里，按表名而非按聚合组织。刷卡有自己的生命周期
// （绑卡、扣款、累计时长、退款），归在一起才能看清它改哪些状态。

// OnlineCard 映射在线卡绑定记录，以规范卡号关联用户及可用状态，协议卡号范围仍由刷卡准入校验。
type OnlineCard struct {
	ID     uint64 `json:"id"`             // 在线卡主键
	CardNo string `json:"card_no"`        // 绑定卡号的规范十进制文本；协议刷卡仍校验其原32位范围
	UserID uint64 `json:"user_id,string"` // 绑定用户主键，响应按十进制字符串传输
	Status string `json:"status"`         // 卡状态：active/lost/disabled/unbound
}

// CardCharge 保存刷卡订单的占用、累计购买事实与首购执行快照；续购沿用冻结套餐和上限，不读取当前价格替换它们。
type CardCharge struct {
	ChargeOrderID    uint64  // 原充电订单主键，会话不存在时读取投影为零
	CardID           uint64  // 原绑定卡主键
	PortCode         string  // 真实端口业务编码，冻结后不可替换
	ActivePort       *string // 当前占用端口，结束／启动失败释放为 nil，唯一键串行占用
	PaidCents        int64   // 会话累计实付金额，单位分
	PurchasedMinutes uint16  // 累计购买时长，单位分钟
	MaxMinutes       uint16  // 首次购买冻结的累计上限，单位分钟
	CardNo           string  // 原协议卡号的十进制文本，首购后保持绑定身份
	WalletAfterCents int64   // 首次扣款后的可花费余额，分，供原 START 使用
	PackageJSON      []byte  // 首次完整套餐 JSON，续购不使用当前发布价格
}

// CardOperation 保存一次首购或续购的扣款凭据，以设备和原事件判重；confirming 状态不表示设备已确认启动。
type CardOperation struct {
	OperationID   string `json:"operation_id"`    // 原操作 UUID，同事件回复和资金凭据
	DeviceID      string `json:"device_id"`       // 原设备身份，与 EventID 共同判重
	EventID       string `json:"event_id"`        // 原刷卡事件号，重试不得替换
	ChargeOrderID uint64 `json:"charge_order_id"` // 原操作所属充电订单主键
	CardID        uint64 `json:"card_id"`         // 原绑定卡主键
	Kind          string `json:"kind"`            // start 首购或 extend 续购
	PriceCents    int64  `json:"price_cents"`     // 本次扣款金额，单位分
	Minutes       uint16 `json:"minutes"`         // 本次购买时长，单位分钟
	Status        string `json:"status"`          // 操作状态：confirming/confirmed/failed
}

// OnlineCardAuditInput 是一次在线卡变更的审计内容。
type OnlineCardAuditInput struct {
	CardID  uint64 // 原绑定卡主键
	ActorID uint64 // 执行变更的用户或管理员主键
	Action  string // bind 绑定 / active 启用 / lost 挂失 / disabled 停用 / unbound 解绑
	Detail  string // 人工依据
}

// CardChargeSession 是刷卡起充的执行快照。起充授权要靠它判断这笔单是
// 刷卡还是扫码，以及卡号与扣款后的钱包余额。
type CardChargeSession struct {
	ChargeOrderID    uint64 // 原充电订单主键，会话不存在时读取投影为零
	CardNo           string // 原协议卡号的十进制文本，首购后保持绑定身份
	WalletAfterCents int64  // 首购后可花费金额，单位分，供原设备 START 使用
}

// TableName 返回刷卡会话所属表；无参数、错误或 IO。
func (CardCharge) TableName() string { return "card_charge" }

// TableName 返回刷卡操作所属表；无参数、错误或 IO。
func (CardOperation) TableName() string { return "card_operation" }

// FindCardChargeSession 在 ctx 下用 db 读取 chargeOrderID 原刷卡执行快照，返回投影或存储错误。
// 无会话返回零主键和 nil，不持有业务锁、不提交或改写余额，调用方据此判断非刷卡订单。
func FindCardChargeSession(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (CardChargeSession, error) {
	var row CardChargeSession
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}

// TableName 返回在线卡所属表；无参数、错误或 IO。
func (OnlineCard) TableName() string { return "online_card" }
