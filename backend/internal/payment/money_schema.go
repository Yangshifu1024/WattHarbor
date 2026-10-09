package payment

import (
	"database/sql"
	"time"
)

// PaymentOrderRecord 是支付所属持久模型；退款和费用回执由 settlement 持有。
type PaymentOrderRecord struct {
	ID                  uint64         `gorm:"column:id;primaryKey"`            // 支付主键，与 UTC 月分区共同标识记录
	OrderNo             string         `gorm:"column:order_no"`                 // 商户支付单号，重试沿用
	BizType             string         `gorm:"column:biz_type"`                 // charge 或 wallet_recharge，决定到账用例
	BizID               uint64         `gorm:"column:biz_id"`                   // 业务 ID；未建充电订单或充值时为 0
	UserID              uint64         `gorm:"column:user_id"`                  // 支付所属用户，必须与通知及意图一致
	PayMethod           string         `gorm:"column:pay_method"`               // 支付渠道，保留发起时值
	TotalCents          int64          `gorm:"column:total_cents"`              // 申请总额，分，到账金额须严格匹配
	PaidCents           int64          `gorm:"column:paid_cents"`               // 已确认到账金额，分
	RefundedCents       int64          `gorm:"column:refunded_cents"`           // 渠道已确认退回的累计金额，分
	WechatTransactionID sql.NullString `gorm:"column:wechat_transaction_id"`    // 已确认渠道交易号；空表示尚未到账
	Status              string         `gorm:"column:status"`                   // 支付状态 initiated、paid、partial_refunded 或 refunded 等
	PaidAt              sql.NullTime   `gorm:"column:paid_at"`                  // 渠道确认 UTC 时刻；未到账为空
	ExpiredAt           sql.NullTime   `gorm:"column:expired_at"`               // 支付有效期；无期限为空
	CreatedMonth        time.Time      `gorm:"column:created_month;primaryKey"` // UTC 月首分区键，重试沿用创建月份
}

// TableName 返回 PaymentOrderRecord 所属表名。
func (PaymentOrderRecord) TableName() string { return "payment_order" }

// PaymentIntentRecord 冻结支付申请的端口、价格与关联；回调只能按标识到账或转退款。
type PaymentIntentRecord struct {
	OfferID            sql.NullInt64 `gorm:"column:offer_id"`             // 冻结套餐 ID；历史非套餐意图可空
	IntentID           string        `gorm:"column:intent_id;primaryKey"` // 支付意图 UUID，业务主键
	ClientRequestID    string        `gorm:"column:client_request_id"`    // 用户范围内的客户端 UUID，相同内容重放
	MerchantOrderNo    string        `gorm:"column:merchant_order_no"`    // 与支付单相同的商户单号
	PaymentOrderID     uint64        `gorm:"column:payment_order_id"`     // 关联支付主键，不允许回调换单
	UserID             uint64        `gorm:"column:user_id"`              // 支付所属用户，必须与通知及意图一致
	OpenID             string        `gorm:"column:openid"`               // 付款人微信身份，敏感值不得公开
	DeviceID           string        `gorm:"column:device_id"`            // 冻结目标设备编号
	PortNo             uint8         `gorm:"column:port_no"`              // 冻结设备端口号，从 1 开始
	PortCode           string        `gorm:"column:port_code"`            // 冻结端口业务编码
	StationID          uint64        `gorm:"column:station_id"`           // 冻结站点身份
	PricingRuleID      uint64        `gorm:"column:pricing_rule_id"`      // 首次申请采用的规则主键
	PricingRuleVersion uint32        `gorm:"column:pricing_rule_version"` // 冻结规则版本，不随发布变化
	PricingSnapshot    []byte        `gorm:"column:pricing_snapshot"`     // 首次申请的完整规则、套餐与费用估计 JSON
	EstimatedKWh       string        `gorm:"column:estimated_kwh"`        // 预估电量十进制文本，单位 kWh
	EstimatedMinutes   uint16        `gorm:"column:estimated_minutes"`    // 预估时长，分钟
	ElectricCents      int64         `gorm:"column:electric_cents"`       // 冻结预估电费，分
	ServiceCents       int64         `gorm:"column:service_cents"`        // 冻结预估服务费，分
	TotalCents         int64         `gorm:"column:total_cents"`          // 优惠前估计总费，分；实付为该值减 DiscountCents
	CouponGrantID      uint64        `gorm:"column:coupon_grant_id"`      // 冻结优惠券权益 ID，0 表示无券
	DiscountCents      int64         `gorm:"column:discount_cents"`       // 冻结优惠减免，分
	ChargeMode         uint8         `gorm:"column:charge_mode"`          // 设备执行模式，沿用冻结套餐定义
	ChargeQuantity     uint16        `gorm:"column:charge_quantity"`      // 设备执行数量，单位由 ChargeMode 决定
	Status             string        `gorm:"column:status"`               // initiated 待付、expired 过期、paid 正常到账或 refund_required 迟到需退款
	ExpiresAt          time.Time     `gorm:"column:expires_at"`           // 意图有效期 UTC 时刻，过期付款转退款
	PaidAt             sql.NullTime  `gorm:"column:paid_at"`              // 渠道确认 UTC 时刻；未到账为空
	ChargeOrderID      sql.NullInt64 `gorm:"column:charge_order_id"`      // 到账后创建的充电订单；迟到支付或未到账为空
}

// TableName 返回 PaymentIntentRecord 所属表名。
func (PaymentIntentRecord) TableName() string { return "charge_payment_intent" }

// PaymentCallbackDigestRecord 以渠道交易唯一键仲裁重复通知及不同身份载荷。
type PaymentCallbackDigestRecord struct {
	WechatTransactionID string `gorm:"column:wechat_transaction_id;primaryKey"` // 已验签渠道交易号，非空且唯一
	RequestDigest       string `gorm:"column:request_digest"`                   // 验签交易编号及金额的 SHA256 摘要，不含通知时间
}

// TableName 返回 PaymentCallbackDigestRecord 所属表名。
func (PaymentCallbackDigestRecord) TableName() string { return "payment_callback_idempotent" }

// ChargePrepayRecord 保存支付首次渠道参数，唯一键保持并发重试身份一致。
type ChargePrepayRecord struct {
	PaymentOrderID uint64         `gorm:"column:payment_order_id;primaryKey"` // 关联支付主键，不允许回调换单
	ParamsJSON     []byte         `gorm:"column:params_json"`                 // 首次预支付参数 JSON，重复准备复用
	PrepayID       sql.NullString `gorm:"column:prepay_id"`                   // 渠道预支付编号，可空
}

// TableName 返回 ChargePrepayRecord 所属表名。
func (ChargePrepayRecord) TableName() string { return "charge_prepay" }
