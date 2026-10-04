package payment

import (
	"context"
	"errors"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CheckoutFailure 表示支付准备失败的业务阶段；HTTP 在适配层决定状态码与文案。
type CheckoutFailure string

const (
	CheckoutInvalidInput   CheckoutFailure = "input"           // 无效请求身份或端口
	CheckoutProvider       CheckoutFailure = "provider"        // 渠道未配置
	CheckoutNotFound       CheckoutFailure = "not_found"       // 扫描端口不存在
	CheckoutGateway        CheckoutFailure = "gateway"         // 设备事实读取失败
	CheckoutPaused         CheckoutFailure = "paused"          // 设备暂停运营
	CheckoutPort           CheckoutFailure = "port"            // 物理端口不可用
	CheckoutPricing        CheckoutFailure = "pricing"         // 无有效计价方案
	CheckoutPricingStorage CheckoutFailure = "pricing_storage" // 方案数据读取失败
	CheckoutCompatibility  CheckoutFailure = "compatibility"   // 方案与设备计量不匹配
	CheckoutOffer          CheckoutFailure = "offer"           // 选择的套餐已下架
	CheckoutPersistence    CheckoutFailure = "persistence"     // 请求重放或预占存储失败
	CheckoutPrepay         CheckoutFailure = "prepay"          // 已保存申请，渠道参数准备失败
)

// CheckoutError 保留失败阶段和原始原因，便于适配器保持公开错误契约。
type CheckoutError struct {
	Kind  CheckoutFailure // 失败阶段，不携带 HTTP 状态
	Cause error           // 原业务或存储错误，用于 errors.Is／As
}

// Error 返回原因或阶段文本，不返回渠道密钥、用户 openid 或请求凭据。
func (e *CheckoutError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}

// Unwrap 返回原错误供调用方匹配领域冲突，不改变失败阶段。
func (e *CheckoutError) Unwrap() error { return e.Cause }

// CheckoutPorts 只提供运营状态已经合并的设备发现结果。
type CheckoutPorts interface {
	// ResolveScan 在 context 下读取原端口编码；错误时结果不能用于创建支付。
	ResolveScan(context.Context, string) (money.ScanResult, error)
}

// CheckoutPricingReader 提供当前有效规则及设备计量兼容检查，不提供管理写入。
type CheckoutPricingReader interface {
	// ActiveDeviceRule 读取 context 下 stationID／deviceID 的完整冻结规则，失败返回错误。
	ActiveDeviceRule(context.Context, uint64, string) (charging.Rule, error)
	// CheckDeviceScheme 校验 context 下目标设备能实现给定方案，错误说明不兼容或读取失败。
	CheckDeviceScheme(context.Context, uint64, string, charging.Scheme) error
}

// CheckoutIntents 提供支付请求凭据、资金预占及渠道参数的持久能力。
type CheckoutIntents interface {
	// Replay 按用户、请求、端口、套餐、券身份复用原请求；nil 表示尚未创建，异内容返回冲突。
	Replay(context.Context, uint64, string, string, uint64, uint64) (*PaymentIntent, error)
	// Reserve 按输入在完整事务中创建原支付身份、套餐及金额快照；失败整体回滚。
	Reserve(context.Context, IntentInput) (PaymentIntent, error)
	// PrepayParams 返回 paymentID 已保存的渠道参数；无参数返回 gorm.ErrRecordNotFound。
	PrepayParams(context.Context, uint64) (PrepayParams, error)
	// SavePrepay 首写 paymentID 的参数；重复保存使用原唯一键，错误不得声称已准备付款。
	SavePrepay(context.Context, uint64, PrepayParams) error
}

// CheckoutService 拥有报价、支付申请重放、准入与提交后预支付准备。
type CheckoutService struct {
	Ports    CheckoutPorts         // 设备发现能力，cmd 注入，不持有网络配置
	Pricing  CheckoutPricingReader // 原有效方案与计量约束
	Intents  CheckoutIntents       // 支付所属请求及预支付 repo
	Provider PrepayProvider        // 通过渠道服务器准备支付参数，不推进订单状态
}

// CheckoutInput 是客户端原申请身份，金额由冻结套餐确定，不能由用户提交。
type CheckoutInput struct {
	ClientRequestID string `json:"client_request_id"` // 客户端 UUID，用户范围内唯一
	PortID          string `json:"port_id"`           // 二维码端口业务编码
	OfferID         uint64 `json:"offer_id"`          // 后台发布套餐身份
}

// CheckoutOffers 保留匿名报价响应；不创建支付、订单或用户会话。
type CheckoutOffers struct {
	StationID    uint64           `json:"station_id"`     // 中央站点身份
	PortID       string           `json:"port_id"`        // 原请求端口编码
	Items        []charging.Offer `json:"items"`          // 当前套餐，金额分、时长分钟、电量按原领域单位
	Display      charging.Display `json:"display"`        // 原方案展示配置
	StopWhenFull bool             `json:"stop_when_full"` // 原充满停止策略
}

// CheckoutResult 保持公开支付准备响应，不包含内部支付主键与 openid。
type CheckoutResult struct {
	IntentID        string       `json:"intent_id"`         // 原支付意图身份
	MerchantOrderNo string       `json:"merchant_order_no"` // 原商户支付单号
	PortID          string       `json:"port_id"`           // 冻结端口编码
	DeviceID        string       `json:"device_id"`         // 冻结设备身份
	StationID       uint64       `json:"station_id"`        // 冻结站点身份
	ElectricCents   int64        `json:"electric_cents"`    // 预估电费，分
	ServiceCents    int64        `json:"service_cents"`     // 预估服务费，分
	TotalCents      int64        `json:"total_cents"`       // 优惠前总额，分
	DiscountCents   int64        `json:"discount_cents"`    // 已冻结优惠额，分
	PayableCents    int64        `json:"payable_cents"`     // 应付金额，分
	CouponGrantID   uint64       `json:"coupon_grant_id"`   // 原券权益身份，无券为 0
	PaymentParams   PrepayParams `json:"payment_params"`    // 渠道支付参数，重试复用原保存结果
	ExpiresAt       time.Time    `json:"expires_at"`        // 原支付期限，UTC
}

// Offers 在 ctx 下读取 portID 的匿名报价；错误带失败阶段，不创建任何资金凭据。
func (s CheckoutService) Offers(ctx context.Context, portID string) (CheckoutOffers, error) {
	port, err := s.port(ctx, portID)
	if err != nil {
		return CheckoutOffers{}, err
	}
	if port.Kind != "port" || port.Port == nil {
		return CheckoutOffers{}, &CheckoutError{Kind: CheckoutNotFound}
	}
	if port.DeviceStatus != "enabled" {
		return CheckoutOffers{}, &CheckoutError{Kind: CheckoutPaused}
	}
	if s.Pricing == nil {
		return CheckoutOffers{}, &CheckoutError{Kind: CheckoutPricingStorage}
	}
	rule, err := s.Pricing.ActiveDeviceRule(ctx, port.StationID, port.DeviceID)
	if err != nil || rule.Spec.Scheme == nil {
		return CheckoutOffers{}, &CheckoutError{Kind: CheckoutPricingStorage, Cause: err}
	}
	if err := s.Pricing.CheckDeviceScheme(ctx, port.StationID, port.DeviceID, *rule.Spec.Scheme); err != nil {
		return CheckoutOffers{}, &CheckoutError{Kind: CheckoutCompatibility, Cause: err}
	}
	return CheckoutOffers{StationID: port.StationID, PortID: portID, Items: rule.Spec.Scheme.Offers(rule), Display: rule.Spec.Scheme.Display, StopWhenFull: rule.Spec.Scheme.Stop.StopWhenFull}, nil
}

// port 在 ctx 下读取 code；网关身份不存在与读取故障独立返回，缺配置关闭准入。
func (s CheckoutService) port(ctx context.Context, code string) (money.ScanResult, error) {
	if s.Ports == nil {
		return money.ScanResult{}, &CheckoutError{Kind: CheckoutGateway}
	}
	out, err := s.Ports.ResolveScan(ctx, code)
	if errors.Is(err, gatewaycontract.ErrScanNotFound) {
		return out, &CheckoutError{Kind: CheckoutNotFound, Cause: err}
	}
	if err != nil {
		return out, &CheckoutError{Kind: CheckoutGateway, Cause: err}
	}
	return out, nil
}

// Start 为 userID 的 in 准备付款；ctx 管理数据库及渠道调用，错误保留已持久申请以便原号恢复。
// 先重放原请求再读当前设备和方案，避免当前发布变化覆盖原套餐与金额。
// Reserve 完成后才请求渠道；预支付成功不表示到账，不创建充电中状态。
func (s CheckoutService) Start(ctx context.Context, userID uint64, in CheckoutInput) (CheckoutResult, error) {
	if s.Provider == nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutProvider}
	}
	if userID == 0 || uuid.Validate(in.ClientRequestID) != nil || in.PortID == "" || in.OfferID == 0 {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutInvalidInput}
	}
	if s.Intents == nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPersistence}
	}
	prior, err := s.Intents.Replay(ctx, userID, in.ClientRequestID, in.PortID, in.OfferID, 0)
	if err != nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPersistence, Cause: err}
	}
	if prior != nil {
		return s.prepare(ctx, *prior)
	}
	port, err := s.port(ctx, in.PortID)
	if err != nil {
		return CheckoutResult{}, err
	}
	if port.DeviceStatus != "enabled" {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPaused}
	}
	if port.Kind != "port" || port.Port == nil || !port.Port.Available || port.StationID == 0 {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPort}
	}
	if s.Pricing == nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPricingStorage}
	}
	rule, err := s.Pricing.ActiveDeviceRule(ctx, port.StationID, port.DeviceID)
	if errors.Is(err, charging.ErrRuleUnavailable) || errors.Is(err, charging.ErrInvalidPricing) {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPricing, Cause: err}
	}
	if err != nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPricingStorage, Cause: err}
	}
	var selected *charging.Offer
	if rule.Spec.Scheme != nil {
		for _, offer := range rule.Spec.Scheme.Offers(rule) {
			if offer.ID == in.OfferID {
				selected = &offer
				break
			}
		}
	}
	if selected == nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutOffer, Cause: charging.ErrOfferUnavailable}
	}
	if err := s.Pricing.CheckDeviceScheme(ctx, port.StationID, port.DeviceID, *rule.Spec.Scheme); err != nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutCompatibility, Cause: err}
	}
	intent, err := s.Intents.Reserve(ctx, IntentInput{UserID: userID, ClientRequestID: in.ClientRequestID, Port: port, Rule: rule, Offer: selected})
	if err != nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPersistence, Cause: err}
	}
	return s.prepare(ctx, intent)
}

// prepare 为 intent 复用或首写预支付参数；ctx 控制 IO，失败不撤销原已提交申请。
// 成功保存后再次读取原参数，使并发不同渠道响应仍返回唯一键的权威记录。
func (s CheckoutService) prepare(ctx context.Context, intent PaymentIntent) (CheckoutResult, error) {
	params, err := s.Intents.PrepayParams(ctx, intent.PaymentOrderID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		params, err = s.Provider.Prepay(ctx, PrepayRequest{MerchantOrderNo: intent.MerchantOrderNo, OpenID: intent.OpenID, AmountCents: intent.PayableCents, ExpiresAt: intent.ExpiresAt})
		if err == nil {
			err = s.Intents.SavePrepay(ctx, intent.PaymentOrderID, params)
		}
		if err == nil {
			params, err = s.Intents.PrepayParams(ctx, intent.PaymentOrderID)
		}
	}
	if err != nil {
		return CheckoutResult{}, &CheckoutError{Kind: CheckoutPrepay, Cause: err}
	}
	return CheckoutResult{IntentID: intent.IntentID, MerchantOrderNo: intent.MerchantOrderNo, PortID: intent.PortCode, DeviceID: intent.DeviceID, StationID: intent.StationID, ElectricCents: intent.Estimate.ElectricCents, ServiceCents: intent.Estimate.ServiceCents, TotalCents: intent.Estimate.TotalCents, DiscountCents: intent.DiscountCents, PayableCents: intent.PayableCents, CouponGrantID: intent.CouponGrantID, PaymentParams: params, ExpiresAt: intent.ExpiresAt.UTC()}, nil
}
