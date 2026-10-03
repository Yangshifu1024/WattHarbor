package card

import (
	"context"

	"gorm.io/gorm"
)

// 刷卡相关的表模型：在线卡、刷卡会话与刷卡操作记录。
// 这些原先散在 database 的 money_schema.go / admin_card_alert.go /
// order_authorization.go 里，按表名而非按聚合组织。刷卡有自己的生命周期
// （绑卡、扣款、累计时长、退款），归在一起才能看清它改哪些状态。

type OnlineCard struct {
	ID     uint64 `json:"id"`
	CardNo string `json:"card_no"`
	UserID uint64 `json:"user_id,string"`
	Status string `json:"status"`
}

type CardCharge struct {
	ChargeOrderID    uint64
	CardID           uint64
	PortCode         string
	ActivePort       *string
	PaidCents        int64
	PurchasedMinutes uint16
	MaxMinutes       uint16
	CardNo           string
	WalletAfterCents int64
	PackageJSON      []byte
}
type CardOperation struct {
	OperationID   string `json:"operation_id"`
	DeviceID      string `json:"device_id"`
	EventID       string `json:"event_id"`
	ChargeOrderID uint64 `json:"charge_order_id"`
	CardID        uint64 `json:"card_id"`
	Kind          string `json:"kind"`
	PriceCents    int64  `json:"price_cents"`
	Minutes       uint16 `json:"minutes"`
	Status        string `json:"status"`
}

// OnlineCardAuditInput 是一次在线卡变更的审计内容。
type OnlineCardAuditInput struct {
	CardID  uint64
	ActorID uint64
	Action  string // bind 绑定 / active 启用 / lost 挂失 / disabled 停用 / unbound 解绑
	Detail  string // 人工依据
}

// CardChargeSession 是刷卡起充的执行快照。起充授权要靠它判断这笔单是
// 刷卡还是扫码，以及卡号与扣款后的钱包余额。
type CardChargeSession struct {
	ChargeOrderID    uint64
	CardNo           string
	WalletAfterCents int64
}

func (CardCharge) TableName() string { return "card_charge" }

func (CardOperation) TableName() string { return "card_operation" }

// FindCardChargeSession 取订单的刷卡执行快照。
// ChargeOrderID 为 0 表示这笔单不是刷卡起充。
func FindCardChargeSession(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (CardChargeSession, error) {
	var row CardChargeSession
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}
