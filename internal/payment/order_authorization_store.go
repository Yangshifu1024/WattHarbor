package payment

import (
	"context"
	"database/sql"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
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

// UserOrderHistoryRow 是用户充电历史里的一行。费用三项为 nil 表示尚未计费完成，
// 不能用 0 顶替——"还没算出来"和"算出��是 0"对用户是两件事。
type UserOrderHistoryRow struct {
	OrderID        uint64     `json:"order_id" gorm:"column:id"`
	OrderNo        string     `json:"order_no" gorm:"column:order_no"`
	DeviceID       string     `json:"device_id" gorm:"column:device_id"`
	PortNo         uint8      `json:"port_no" gorm:"column:port_no"`
	Status         string     `json:"status" gorm:"column:status"`
	StartedAt      *time.Time `json:"started_at" gorm:"column:started_at"`
	EndedAt        *time.Time `json:"ended_at" gorm:"column:ended_at"`
	ChargedKWh     *string    `json:"charged_kwh" gorm:"column:charged_kwh"`
	ChargedSeconds *uint32    `json:"charged_seconds" gorm:"column:charged_seconds"`
	ElectricCents  *int64     `json:"electric_cents" gorm:"column:electric_cents"`
	ServiceCents   *int64     `json:"service_cents" gorm:"column:service_cents"`
	TotalCents     *int64     `json:"total_cents" gorm:"column:total_cents"`
	DiscountCents  int64      `json:"discount_cents" gorm:"column:discount_cents"`
}

// ListUserOrderHistory 分页返回用户的充电历史，可按订单状态过滤。
func ListUserOrderHistory(ctx context.Context, db *gorm.DB, userID uint64, status string, page, pageSize int) (persist.Page[UserOrderHistoryRow], error) {
	out := persist.Page[UserOrderHistoryRow]{Items: []UserOrderHistoryRow{}, Page: page, PageSize: pageSize}
	query := db.WithContext(ctx).Table("charge_order").Where("user_id = ? AND deleted_at IS NULL", userID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	// 费用在 charge_fee_receipt.result_json 里，不在订单表上，所以这三列此刻查不出来，
	// 由调用方按批回填。显式列出列名，避免 GORM 按字段名推导取错列。
	err := query.Select("id, order_no, device_id, port_no, status, started_at, ended_at, " +
		"charged_kwh, charged_seconds, discount_cents").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}

// OrderPaymentBrief 是订单详情里的支付单摘要。
type OrderPaymentBrief struct {
	OrderNo       string `gorm:"column:order_no" json:"order_no"`
	TotalCents    int64  `gorm:"column:total_cents" json:"total_cents"`
	PaidCents     int64  `gorm:"column:paid_cents" json:"paid_cents"`
	RefundedCents int64  `gorm:"column:refunded_cents" json:"refunded_cents"`
	Status        string `gorm:"column:status" json:"status"`
}

// FindOrderPaymentBrief 取支付单摘要。取不到返回 gorm.ErrRecordNotFound，
// 调用方按"没有支付单"处理而不是报错。
func FindOrderPaymentBrief(ctx context.Context, db *gorm.DB, paymentOrderID uint64) (OrderPaymentBrief, error) {
	var row OrderPaymentBrief
	err := db.WithContext(ctx).Table("payment_order").
		Select("order_no, total_cents, paid_cents, refunded_cents, status").
		Where("id = ? AND deleted_at IS NULL", paymentOrderID).Take(&row).Error
	return row, err
}

// ListOrderRefunds 返回订单产生的退款记录，按 ID 倒序。
func ListOrderRefunds(ctx context.Context, db *gorm.DB, orderID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("refund_record").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", orderID).
		Select("refund_no, refund_cents, status, reason, created_at").Order("id DESC").Find(&rows).Error
	return rows, err
}
