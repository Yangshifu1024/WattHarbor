package database

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 起充与停机的授权查询。这两个接口决定"能不能对某个端口下发指令"，
// 判定条件都写死在查询里：条件放宽或收紧都会直接变成越权下发。
//
// 起充：订单与支付单都要实付且金额匹配，充值的电量/时长在合法区间内。
// 停机：订单必须正在充电、属于该用户、且已有成功的起充回执带回了端口身份。

// PaidStartAuthorization 是一笔可授权起充的订单。
type PaidStartAuthorization struct {
	ChargeOrderID  uint64
	OrderNo        string
	UserID         uint64
	DeviceID       string
	PortNo         uint8
	PaymentOrderID uint64
	ChargeMode     uint8
	ChargeQuantity uint16
}

// FindPaidStartAuthorization 取一笔已付清且满足计量区间约束的订单。
// 取不到返回 gorm.ErrRecordNotFound，调用方据此拒绝下发。
func FindPaidStartAuthorization(ctx context.Context, db *gorm.DB, orderNo string) (PaidStartAuthorization, error) {
	var row PaidStartAuthorization
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select(`c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no,
			p.id AS payment_order_id, c.charge_mode, c.charge_quantity`).
		Joins(`JOIN payment_order AS p ON p.id = c.payment_order_id
			AND p.biz_type = 'charge' AND p.biz_id = c.id AND p.user_id = c.user_id`).
		Where(`c.order_no = ? AND c.status = 'paid' AND c.deleted_at IS NULL
			AND p.status = 'paid' AND p.paid_cents >= p.total_cents
			AND p.total_cents > 0 AND p.deleted_at IS NULL
			AND ((c.charge_mode IN (0,4) AND c.charge_quantity BETWEEN 1 AND 4320)
			OR (c.charge_mode = 1 AND c.charge_quantity BETWEEN 1000 AND 65000))`, orderNo).
		Take(&row).Error
	return row, err
}

// CardChargeSession 是刷卡起充的执行快照。起充授权要靠它判断这笔单是
// 刷卡还是扫码，以及卡号与扣款后的钱包余额。
type CardChargeSession struct {
	ChargeOrderID    uint64
	CardNo           string
	WalletAfterCents int64
}

// FindCardChargeSession 取订单的刷卡执行快照。
// ChargeOrderID 为 0 表示这笔单不是刷卡起充。
func FindCardChargeSession(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (CardChargeSession, error) {
	var row CardChargeSession
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}

// ActiveStopAuthorization 是一笔可授权停机的订单。
type ActiveStopAuthorization struct {
	ChargeOrderID  uint64
	OrderNo        string
	UserID         uint64
	DeviceID       string
	PortNo         uint8
	PortID         sql.NullInt64 // 端口身份；NULL 表示起充回执没带回端口，不能下发停机
	StartCommandID string        // 起充指令 ID，回报停机结果时带回
}

// FindActiveStopAuthorization 取一笔正在充电、属于该用户、且已有成功起充回执的订单。
func FindActiveStopAuthorization(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (ActiveStopAuthorization, error) {
	var row ActiveStopAuthorization
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select("c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no, s.port_id, s.command_id AS start_command_id").
		Joins("JOIN charge_start_receipt AS s ON s.charge_order_id = c.id AND s.success = TRUE").
		Where("c.order_no = ? AND c.user_id = ? AND c.status = 'charging' AND c.deleted_at IS NULL AND s.port_id IS NOT NULL", orderNo, userID).
		Take(&row).Error
	return row, err
}

// RecordBillingCutoff 记下计费截止时刻：停机之后发生的电量不再计入本单。
// 重复停机只刷新时刻不改 charge_order_id，所以用 no-op upsert。
func RecordBillingCutoff(ctx context.Context, db *gorm.DB, chargeOrderID uint64, reason string, at time.Time) error {
	return db.WithContext(ctx).Table("charge_billing_cutoff").
		Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).
		Create(map[string]any{"charge_order_id": chargeOrderID, "cutoff_at": at, "reason": reason}).Error
}

// BillingCutoff 是一笔订单的计费截止记录。取不到表示还没截止。
// cutoff_at 按文本返回：未截止与已截止在这里都是"有值"，时刻的解析交给调用方。
type BillingCutoff struct {
	CutoffAt *string `json:"cutoff_at" gorm:"column:cutoff_at"`
	Reason   string  `json:"reason" gorm:"column:reason"`
}

// FindBillingCutoff 返回订单的计费截止时刻。
func FindBillingCutoff(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingCutoff, error) {
	var row BillingCutoff
	err := db.WithContext(ctx).Table("charge_billing_cutoff").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}

// BillingJobState 是计费任务的排队状态。取不到表示尚未排队。
type BillingJobState struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	Status        string `json:"status" gorm:"column:status"`
}

// FindBillingJob 返回订单的计费排队状态。
func FindBillingJob(ctx context.Context, db *gorm.DB, chargeOrderID uint64) (BillingJobState, error) {
	var row BillingJobState
	err := db.WithContext(ctx).Table("charge_billing_job").
		Where("charge_order_id=?", chargeOrderID).Find(&row).Error
	return row, err
}
