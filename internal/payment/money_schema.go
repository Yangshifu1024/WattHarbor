package payment

import (
	"database/sql"
	"encoding/json"
	"time"
)

// 这批类型原先放在 domain/money，注释写着"承载表记录类型"——但它们带
// gorm 列映射和 TableName()，是持久化模型而不是业务概念。领域层一旦开始
// 标注列名，规则与单位就跟着库结构走，改表要改业务代码。
//
// 判据不是"import 了 gorm 就算违规"：gorm:"column:" 与 TableName() 都只是
// 字符串与普通方法，不 import gorm 也能写。只查 import 的护栏会让它们从
// 缝里溜过去，因此 layers.sh 现在同时拦列映射与表名映射。

// ChargeFeeRecord 保存订单结算回执，费用明细在计价引擎输出的 result_json 中。
type ChargeFeeRecord struct {
	ChargeOrderID  uint64 `gorm:"column:charge_order_id;primaryKey"`
	CalculationNo  string `gorm:"column:calculation_no"`
	ResultJSON     []byte `gorm:"column:result_json"`
	ShortfallCents int64  `gorm:"column:shortfall_cents"`
}

func (ChargeFeeRecord) TableName() string { return "charge_fee_receipt" }

// Fees 把存下来的费用明细解码成扁平的金额字段。
func (r ChargeFeeRecord) Fees() (electric, service, total int64, ok bool) {
	// 回执存的是完整的计费 Result，
	// 其中费用字段就在顶层，与 source 块并列。
	var flat struct {
		ElectricCents int64 `json:"electric_cents"`
		ServiceCents  int64 `json:"service_cents"`
		TotalCents    int64 `json:"total_cents"`
	}
	if err := json.Unmarshal(r.ResultJSON, &flat); err != nil {
		return 0, 0, 0, false
	}
	return flat.ElectricCents, flat.ServiceCents, flat.TotalCents, true
}

type PaymentOrderRecord struct {
	ID                  uint64         `gorm:"column:id;primaryKey"`
	OrderNo             string         `gorm:"column:order_no"`
	BizType             string         `gorm:"column:biz_type"`
	BizID               uint64         `gorm:"column:biz_id"`
	UserID              uint64         `gorm:"column:user_id"`
	PayMethod           string         `gorm:"column:pay_method"`
	TotalCents          int64          `gorm:"column:total_cents"`
	PaidCents           int64          `gorm:"column:paid_cents"`
	RefundedCents       int64          `gorm:"column:refunded_cents"`
	WechatTransactionID sql.NullString `gorm:"column:wechat_transaction_id"`
	Status              string         `gorm:"column:status"`
	PaidAt              sql.NullTime   `gorm:"column:paid_at"`
	ExpiredAt           sql.NullTime   `gorm:"column:expired_at"`
	CreatedMonth        time.Time      `gorm:"column:created_month;primaryKey"`
}

func (PaymentOrderRecord) TableName() string { return "payment_order" }

type PaymentIntentRecord struct {
	OfferID            sql.NullInt64 `gorm:"column:offer_id"`
	IntentID           string        `gorm:"column:intent_id;primaryKey"`
	ClientRequestID    string        `gorm:"column:client_request_id"`
	MerchantOrderNo    string        `gorm:"column:merchant_order_no"`
	PaymentOrderID     uint64        `gorm:"column:payment_order_id"`
	UserID             uint64        `gorm:"column:user_id"`
	OpenID             string        `gorm:"column:openid"`
	DeviceID           string        `gorm:"column:device_id"`
	PortNo             uint8         `gorm:"column:port_no"`
	PortCode           string        `gorm:"column:port_code"`
	StationID          uint64        `gorm:"column:station_id"`
	PricingRuleID      uint64        `gorm:"column:pricing_rule_id"`
	PricingRuleVersion uint32        `gorm:"column:pricing_rule_version"`
	PricingSnapshot    []byte        `gorm:"column:pricing_snapshot"`
	EstimatedKWh       string        `gorm:"column:estimated_kwh"`
	EstimatedMinutes   uint16        `gorm:"column:estimated_minutes"`
	ElectricCents      int64         `gorm:"column:electric_cents"`
	ServiceCents       int64         `gorm:"column:service_cents"`
	TotalCents         int64         `gorm:"column:total_cents"`
	CouponGrantID      uint64        `gorm:"column:coupon_grant_id"`
	DiscountCents      int64         `gorm:"column:discount_cents"`
	ChargeMode         uint8         `gorm:"column:charge_mode"`
	ChargeQuantity     uint16        `gorm:"column:charge_quantity"`
	Status             string        `gorm:"column:status"`
	ExpiresAt          time.Time     `gorm:"column:expires_at"`
	PaidAt             sql.NullTime  `gorm:"column:paid_at"`
	ChargeOrderID      sql.NullInt64 `gorm:"column:charge_order_id"`
}

func (PaymentIntentRecord) TableName() string { return "charge_payment_intent" }

type PaymentCallbackDigestRecord struct {
	WechatTransactionID string `gorm:"column:wechat_transaction_id;primaryKey"`
	RequestDigest       string `gorm:"column:request_digest"`
}

func (PaymentCallbackDigestRecord) TableName() string { return "payment_callback_idempotent" }

type ChargePrepayRecord struct {
	PaymentOrderID uint64         `gorm:"column:payment_order_id;primaryKey"`
	ParamsJSON     []byte         `gorm:"column:params_json"`
	PrepayID       sql.NullString `gorm:"column:prepay_id"`
}

func (ChargePrepayRecord) TableName() string { return "charge_prepay" }

type RefundRecord struct {
	ExecutionPolicy string         `gorm:"column:execution_policy;default:manual_review"`
	NextAttemptAt   time.Time      `gorm:"column:next_attempt_at;default:CURRENT_TIMESTAMP(3)"`
	RetryCount      uint32         `gorm:"column:retry_count"`
	ID              uint64         `gorm:"column:id;primaryKey"`
	RefundNo        string         `gorm:"column:refund_no"`
	PaymentOrderID  uint64         `gorm:"column:payment_order_id"`
	UserID          uint64         `gorm:"column:user_id"`
	BizType         string         `gorm:"column:biz_type"`
	BizID           uint64         `gorm:"column:biz_id"`
	RefundCents     int64          `gorm:"column:refund_cents"`
	Reason          sql.NullString `gorm:"column:reason"`
	Status          string         `gorm:"column:status"`
	CreatedMonth    time.Time      `gorm:"column:created_month;primaryKey"`
}

func (RefundRecord) TableName() string { return "refund_record" }
