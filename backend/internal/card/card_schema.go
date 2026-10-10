package card

import (
	carddomain "chargepilot/internal/domain/card"
	"context"

	"gorm.io/gorm"
)

// 本文件定义在线卡、刷卡会话与操作记录，保存绑定、扣款、累计时长及退款所需信息。

// OnlineCard 保存卡号、绑定用户和卡状态；刷卡用例另行检查协议卡号范围。
type OnlineCard struct {
	ID     uint64     `json:"id"`             // 在线卡主键
	CardNo string     `json:"card_no"`        // 绑定卡号的规范十进制文本；协议刷卡仍校验其32位范围
	UserID uint64     `json:"user_id,string"` // 绑定用户主键，响应按十进制字符串传输
	Status CardStatus `json:"status"`         // online_card.status：active/lost/disabled/unbound
}

// CardCharge 保存刷卡订单的占用、累计购买记录与首购执行快照；续购沿用冻结套餐和上限，不读取当前价格替换它们。
type CardCharge struct {
	ChargeOrderID    uint64  // 充电订单主键，没有会话时返回零值
	CardID           uint64  // 绑定卡主键
	PortCode         string  // 真实端口业务编码，冻结后不可替换
	ActivePort       *string // 当前占用端口，结束、启动失败释放为 nil，唯一键串行占用
	PaidCents        int64   // 会话累计实付金额，单位分
	PurchasedMinutes uint16  // 累计购买时长，单位分钟
	MaxMinutes       uint16  // 首次购买冻结的累计上限，单位分钟
	CardNo           string  // 协议卡号的十进制文本，首购后保持绑定关系
	WalletAfterCents int64   // 首次扣款后的可用余额，分，供 START 使用
	PackageJSON      []byte  // 首次完整套餐 JSON，续购不使用当前发布价格
}

// CardOperation 保存一次首购或续购结果，按设备和事件号判重；confirming 表示仍待启动确认。
type CardOperation struct {
	OperationID   string                         `json:"operation_id"`    // 首次操作 UUID，同事件重试沿用
	DeviceID      string                         `json:"device_id"`       // 设备编号，与 EventID 共同判重
	EventID       string                         `json:"event_id"`        // 刷卡事件号，重试不得替换
	ChargeOrderID uint64                         `json:"charge_order_id"` // 首次操作所属充电订单主键
	CardID        uint64                         `json:"card_id"`         // 绑定卡主键
	Kind          carddomain.CardOperationKind   `json:"kind"`            // start 首购或 extend 续购
	PriceCents    int64                          `json:"price_cents"`     // 本次扣款金额，单位分
	Minutes       uint16                         `json:"minutes"`         // 本次购买时长，单位分钟
	Status        carddomain.CardOperationStatus `json:"status"`          // 操作状态：confirming/confirmed/failed
}

// OnlineCardAuditInput 是一次在线卡变更的审计内容。
type OnlineCardAuditInput struct {
	CardID  uint64 // 绑定卡主键
	ActorID uint64 // 执行变更的用户或管理员主键
	Action  string // bind 绑定 / active 启用 / lost 挂失 / disabled 停用 / unbound 解绑
	Detail  string // 人工依据
}

// CardChargeSession 保存刷卡起充快照，供启动授权读取卡号与扣款后余额。
type CardChargeSession struct {
	ChargeOrderID    uint64 // 充电订单主键，没有会话时返回零值
	CardNo           string // 协议卡号的十进制文本，首购后保持绑定关系
	WalletAfterCents int64  // 首购后可用余额，单位分，供设备 START 使用
}

// TableName 返回刷卡会话所属表。
func (CardCharge) TableName() string { return "card_charge" }

// TableName 返回刷卡操作所属表。
func (CardOperation) TableName() string { return "card_operation" }

// FindCardChargeSession 用 db 读取 chargeOrderID 刷卡执行快照，返回查询结果。
// 没有刷卡会话返回零主键和 nil，不取得业务锁、不提交或改写余额，调用方据此判断非刷卡订单。
func FindCardChargeSession(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (CardChargeSession, error) {
	var row CardChargeSession
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}

// TableName 返回在线卡所属表。
func (OnlineCard) TableName() string { return "online_card" }
