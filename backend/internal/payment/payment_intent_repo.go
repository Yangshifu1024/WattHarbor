package payment

import (
	money "chargepilot/internal/domain/money"
	"chargepilot/internal/marketing"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PaymentIntent 是支付持久凭据向 checkout 返回的冻结业务视图。
type PaymentIntent struct {
	IntentID        string            `json:"intent_id"`                 // 原支付意图 UUID
	MerchantOrderNo string            `json:"merchant_order_no"`         // 原商户支付单号，重试沿用
	PaymentOrderID  uint64            `json:"payment_order_id"`          // 关联支付主键
	UserID          uint64            `json:"user_id,string"`            // 申请所属用户，公开投影为十进制字符串
	OpenID          string            `json:"-"`                         // 原付款人微信身份，不公开
	DeviceID        string            `json:"device_id"`                 // 冻结目标设备编号
	PortNo          uint8             `json:"port_no"`                   // 冻结端口号，从 1 开始
	PortCode        string            `json:"port_id"`                   // 冻结端口业务编码
	StationID       uint64            `json:"station_id"`                // 冻结站点身份
	Estimate        charging.Estimate `json:"estimate"`                  // 首次冻结的费用及设备执行量
	ExpiresAt       time.Time         `json:"expires_at"`                // 原意图有效期 UTC 时刻
	Status          string            `json:"status"`                    // 意图 initiated、expired、paid 或 refund_required
	CouponGrantID   uint64            `json:"coupon_grant_id,omitempty"` // 原优惠券权益 ID，0 表示无券；当前套餐不允许组合券
	DiscountCents   int64             `json:"discount_cents"`            // 冻结减免金额，分
	PayableCents    int64             `json:"payable_cents"`             // 实际应付金额，分，等于优惠前总费减减免
}

// IntentInput 提供首次支付申请的用户、端口及完整套餐身份。
type IntentInput struct {
	UserID          uint64           // 申请所属用户，公开投影为十进制字符串
	ClientRequestID string           // 用户范围内的申请 UUID，同内容重放
	Port            money.ScanResult // 准入时设备及端口事实，必须可用且身份一致
	Energy          string           // 历史非套餐输入电量，kWh 十进制文本；当前仅接受套餐
	Minutes         uint16           // 历史非套餐时长，分钟；当前仅接受套餐
	Rule            charging.Rule    // 当前准入方案及规则版本，首次创建冻结
	Offer           *charging.Offer  // 后台发布的套餐，当前申请必须非空
	// 券身份参与历史请求重放核对；当前套餐创建不允许组合券。
	CouponGrantID uint64 // 原优惠券权益 ID，0 表示无券；当前套餐不允许组合券
}

// PaymentIntentRepo 拥有支付意图、原支付和端口短期预占事务。
type PaymentIntentRepo struct {
	DB *gorm.DB // central 支付连接，由进程管理
}

// Replay 在 ctx 下按 userID、requestID 查询原支付，以 portID、offerID、couponID 核对同内容。
// 返回冻结意图；nil、nil 表示尚未创建，异内容、失效、损坏快照返回冲突，存储错误原样返回。
// 不重新读取当前方案或准入事实，不创建支付或延长原有效期。
func (s PaymentIntentRepo) Replay(ctx context.Context, userID uint64, requestID, portID string, offerID, couponID uint64) (*PaymentIntent, error) {
	if s.DB == nil || userID == 0 || uuid.Validate(requestID) != nil {
		return nil, ErrPaymentIntentConflict
	}
	var row PaymentIntentRecord
	err := s.DB.WithContext(ctx).Where("user_id = ? AND client_request_id = ?", userID, requestID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.PortCode != portID || row.CouponGrantID != couponID || row.Status != "initiated" || !time.Now().Before(row.ExpiresAt) ||
		(row.OfferID.Valid && row.OfferID.Int64 != int64(offerID)) || (!row.OfferID.Valid && offerID != 0) {
		return nil, ErrPaymentIntentConflict
	}
	var snapshot struct {
		Estimate charging.Estimate `json:"estimate"` // 首次冻结的费用及设备执行量
		Offer    *charging.Offer   `json:"offer"`    // 后台发布的套餐，当前申请必须非空
	}
	if json.Unmarshal(row.PricingSnapshot, &snapshot) != nil || snapshot.Estimate.TotalCents != row.TotalCents ||
		(row.OfferID.Valid && (snapshot.Offer == nil || snapshot.Offer.ID != offerID)) {
		return nil, ErrPaymentIntentConflict
	}
	return &PaymentIntent{IntentID: row.IntentID, MerchantOrderNo: row.MerchantOrderNo, PaymentOrderID: row.PaymentOrderID,
		UserID: row.UserID, OpenID: row.OpenID, DeviceID: row.DeviceID, PortNo: row.PortNo, PortCode: row.PortCode,
		StationID: row.StationID, Estimate: snapshot.Estimate, ExpiresAt: row.ExpiresAt, Status: row.Status,
		CouponGrantID: row.CouponGrantID, DiscountCents: row.DiscountCents, PayableCents: row.TotalCents - row.DiscountCents}, nil
}

// Reserve 在 ctx 下按 input 验证原身份、方案及套餐后原子创建支付和短期端口预占。
// 返回冻结意图或输入、同请求内容、端口占用、套餐或存储错误；失败不留下支付。
// READ COMMITTED 事务内锁端口预占，冻结五分钟期限及价格，订单仅由验签到账回调创建。
func (s PaymentIntentRepo) Reserve(ctx context.Context, input IntentInput) (PaymentIntent, error) {
	if s.DB == nil || input.UserID == 0 || uuid.Validate(input.ClientRequestID) != nil ||
		input.Port.Kind != "port" || input.Port.Port == nil || !input.Port.Port.Available ||
		input.Port.StationID == 0 || input.Port.StationID != input.Rule.StationID ||
		input.Port.Port.PortID == "" || input.Port.Port.DeviceID != input.Port.DeviceID || input.Port.Port.PortNo == 0 {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	if input.Offer != nil && input.CouponGrantID != 0 {
		// 套餐返还按实付金额处理。
		// 券的分摊需要另立一份契约，才能与固定价套餐组合。
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	var previous PaymentIntentRecord
	err := s.DB.WithContext(ctx).Where("user_id = ? AND client_request_id = ?", input.UserID, input.ClientRequestID).Take(&previous).Error
	if err == nil {
		return existingIntent(previous, input)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return PaymentIntent{}, err
	}
	var estimate charging.Estimate
	if input.Offer != nil {
		if !input.Offer.Valid() || input.Offer.StationID != input.Port.StationID {
			return PaymentIntent{}, charging.ErrInvalidPricing
		}
		// 固定时长套餐由设备倒计时停机；金额预算由平台根据计量执行计费和停机。
		// 下发的最大时长是设备安全上限，不替代平台的消费封顶规则。
		if input.Rule.Spec.Scheme == nil {
			return PaymentIntent{}, charging.ErrInvalidPricing
		}
		p, ok := input.Rule.Spec.Scheme.Package(input.Offer.PackageID)
		if !ok || input.Offer.ID != input.Rule.ID*100+p.ID || p.PriceCents != input.Offer.PriceCents || p.Mode != input.Offer.Mode {
			return PaymentIntent{}, charging.ErrInvalidPricing
		}
		input.Rule.Spec = input.Rule.Spec.Scheme.SpecFor(p)
		minutes, mode := input.Rule.Spec.Scheme.Normalized().Policy.MaxMinutes, uint8(4)
		quantity := minutes
		if input.Offer.Mode == "duration" {
			minutes, mode = input.Offer.DurationMinutes, 0
			quantity = minutes
		} else if input.Offer.Mode == "energy" {
			minutes, mode, quantity = 0, 1, uint16(input.Offer.EnergyWh)
		}
		estimate = charging.Estimate{Mode: input.Rule.Spec.Mode, EstimatedKWh: "0.000", EstimatedMinutes: minutes, PrepaidCents: input.Offer.PriceCents, TotalCents: input.Offer.PriceCents, ChargeMode: mode, ChargeQuantity: quantity}
	} else {
		return PaymentIntent{}, charging.ErrOfferUnavailable
	}
	// 创建支付记录和占用端口前校验优惠券并计算减免额，失败时不留预占记录。
	var discount int64
	if input.CouponGrantID != 0 {
		coupons := marketing.CouponService{DB: s.DB}
		discount, err = coupons.Quote(ctx, input.UserID, input.CouponGrantID, estimate.TotalCents)
		if err != nil {
			return PaymentIntent{}, err
		}
	}
	intentID := uuid.NewString()
	var merchantOrderNo string
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)
	snapshot, err := json.Marshal(struct {
		Rule       charging.Rule     `json:"rule"`            // 当前准入方案及规则版本，首次创建冻结
		Estimate   charging.Estimate `json:"estimate"`        // 首次冻结的费用及设备执行量
		Offer      *charging.Offer   `json:"offer,omitempty"` // 后台发布的套餐，当前申请必须非空
		ComputedAt time.Time         `json:"computed_at"`     // 首次冻结方案和执行量的 UTC 时刻
	}{Rule: input.Rule, Estimate: estimate, Offer: input.Offer, ComputedAt: time.Now().UTC()})
	if err != nil {
		return PaymentIntent{}, err
	}
	var openid string
	var paymentID uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 仅释放本次申请端口已过期的预占，与新意图创建同事务，不扫描其他端口。
		if err := tx.Model(&PaymentIntentRecord{}).
			Where("port_code = ? AND status = 'initiated' AND expires_at <= UTC_TIMESTAMP(3)", input.Port.Port.PortID).
			Update("status", "expired").Error; err != nil {
			return err
		}
		if err := CheckoutPortAvailable(tx, input.Port.Port.PortID); err != nil {
			return err
		}
		var identity struct {
			OpenID string `gorm:"column:openid"` // 原付款人微信身份，不公开
		}
		if err := tx.Table("user").Select("openid").Where("id = ? AND status = 'active' AND deleted_at IS NULL", input.UserID).Take(&identity).Error; err != nil {
			return err
		}
		openid = identity.OpenID
		merchantOrderNo, err = NewOrderNumber(tx)
		if err != nil {
			return err
		}
		payable := estimate.TotalCents - discount
		paymentOrder := PaymentOrderRecord{OrderNo: merchantOrderNo, BizType: "charge", BizID: 0,
			UserID: input.UserID, PayMethod: "wechat", TotalCents: payable,
			Status: "initiated", ExpiredAt: sql.NullTime{Time: expiresAt, Valid: true}, CreatedMonth: dbutil.MonthStart()}
		if err := tx.Create(&paymentOrder).Error; err != nil {
			return err
		}
		paymentID = paymentOrder.ID
		intent := PaymentIntentRecord{IntentID: intentID, ClientRequestID: input.ClientRequestID,
			MerchantOrderNo: merchantOrderNo, PaymentOrderID: paymentID, UserID: input.UserID, OpenID: openid,
			DeviceID: input.Port.DeviceID, PortNo: input.Port.Port.PortNo, PortCode: input.Port.Port.PortID,
			StationID: input.Port.StationID, PricingRuleID: input.Rule.ID, PricingRuleVersion: input.Rule.Version,
			PricingSnapshot: snapshot, EstimatedKWh: estimate.EstimatedKWh, EstimatedMinutes: estimate.EstimatedMinutes,
			ElectricCents: estimate.ElectricCents, ServiceCents: estimate.ServiceCents, TotalCents: estimate.TotalCents,
			CouponGrantID: input.CouponGrantID, DiscountCents: discount,
			ChargeMode: estimate.ChargeMode, ChargeQuantity: estimate.ChargeQuantity, Status: "initiated", ExpiresAt: expiresAt}
		if input.Offer != nil {
			intent.OfferID = sql.NullInt64{Int64: int64(input.Offer.ID), Valid: true}
		}
		if err := tx.Create(&intent).Error; err != nil {
			if dbutil.IsMySQLDuplicate(err) {
				return ErrPaymentIntentConflict
			}
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PaymentIntent{}, err
	}
	if paymentID == 0 {
		return PaymentIntent{}, err
	}
	return PaymentIntent{IntentID: intentID, MerchantOrderNo: merchantOrderNo, PaymentOrderID: paymentID, UserID: input.UserID,
		OpenID: openid, DeviceID: input.Port.DeviceID, PortNo: input.Port.Port.PortNo, PortCode: input.Port.Port.PortID,
		StationID: input.Port.StationID, Estimate: estimate, ExpiresAt: expiresAt, Status: "initiated",
		CouponGrantID: input.CouponGrantID, DiscountCents: discount, PayableCents: estimate.TotalCents - discount}, nil
}

// existingIntent 按 input 逐项核对已存在的 row，返回原冻结意图或 ErrPaymentIntentConflict。
// 端口、套餐、券、期限及历史非套餐数量必须一致；不使用当前规则替换原快照，无 IO。
func existingIntent(row PaymentIntentRecord, input IntentInput) (PaymentIntent, error) {
	if row.PortCode != input.Port.Port.PortID || row.DeviceID != input.Port.DeviceID || row.Status != "initiated" || time.Now().After(row.ExpiresAt) {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	if input.Offer != nil {
		if !row.OfferID.Valid || row.OfferID.Int64 != int64(input.Offer.ID) {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
	} else if row.OfferID.Valid || row.EstimatedMinutes != input.Minutes || row.PricingRuleID != input.Rule.ID || row.PricingRuleVersion != input.Rule.Version {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	// 重放必须使用首次意图绑定的优惠券。
	if row.CouponGrantID != input.CouponGrantID {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	var snapshot struct {
		Estimate charging.Estimate `json:"estimate"` // 首次冻结的费用及设备执行量
		Offer    *charging.Offer   `json:"offer"`    // 后台发布的套餐，当前申请必须非空
	}
	if json.Unmarshal(row.PricingSnapshot, &snapshot) != nil {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	if input.Offer != nil {
		if snapshot.Offer == nil || snapshot.Offer.ID != input.Offer.ID {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
	} else {
		requestedEnergy, err := decimal.NewFromString(input.Energy)
		if err != nil || snapshot.Estimate.EstimatedKWh != requestedEnergy.StringFixed(3) {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
	}
	if snapshot.Estimate.TotalCents != row.TotalCents {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	return PaymentIntent{IntentID: row.IntentID, MerchantOrderNo: row.MerchantOrderNo, PaymentOrderID: row.PaymentOrderID,
		UserID: row.UserID, OpenID: row.OpenID, DeviceID: row.DeviceID, PortNo: row.PortNo, PortCode: row.PortCode,
		StationID: row.StationID, Estimate: snapshot.Estimate, ExpiresAt: row.ExpiresAt, Status: row.Status,
		CouponGrantID: row.CouponGrantID, DiscountCents: row.DiscountCents, PayableCents: row.TotalCents - row.DiscountCents}, nil
}

// PrepayParams 在 ctx 下读取 paymentOrderID 的首次参数，返回原参数或存储／载荷错误。
// 无参数返回 gorm.ErrRecordNotFound，渠道和预支付身份不能为空；不请求渠道。
func (s PaymentIntentRepo) PrepayParams(ctx context.Context, paymentOrderID uint64) (PrepayParams, error) {
	var record ChargePrepayRecord
	if err := s.DB.WithContext(ctx).Where("payment_order_id = ?", paymentOrderID).Take(&record).Error; err != nil {
		return PrepayParams{}, err
	}
	var params PrepayParams
	if err := json.Unmarshal(record.ParamsJSON, &params); err != nil || params.PrepayID == "" || params.Provider == "" {
		return PrepayParams{}, ErrPaymentIntentConflict
	}
	return params, nil
}

// SavePrepay 在 ctx 下为 paymentOrderID 首写 params，以支付唯一键忽略并发后写。
// 返回输入、序列化或存储错误；成功不表示传入值赢得首写，调用方须重读原参数。
func (s PaymentIntentRepo) SavePrepay(ctx context.Context, paymentOrderID uint64, params PrepayParams) error {
	if s.DB == nil || paymentOrderID == 0 || params.PrepayID == "" || params.Provider == "" {
		return ErrPaymentIntentConflict
	}
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	record := ChargePrepayRecord{PaymentOrderID: paymentOrderID, ParamsJSON: data, PrepayID: sql.NullString{String: params.PrepayID, Valid: true}}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "payment_order_id"}}, DoNothing: true}).Create(&record).Error
}
