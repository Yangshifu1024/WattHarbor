package payment

import (
	"context"
	"errors"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/money"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CheckoutFailure 表示支付准备失败的业务阶段；HTTP 在适配层决定状态码与文案。
type CheckoutFailure string

const (
	CheckoutInvalidInput   CheckoutFailure = "input"           // 用户、请求号或端口无效
	CheckoutProvider       CheckoutFailure = "provider"        // 渠道未配置
	CheckoutNotFound       CheckoutFailure = "not_found"       // 扫描端口不存在
	CheckoutGateway        CheckoutFailure = "gateway"         // 设备信息读取失败
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
	Cause error           // 业务或存储错误，用于 errors.Is、As
}

// Error 返回原始错误文本；没有原始错误时返回失败阶段。
func (e *CheckoutError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}

// Unwrap 返回错误供调用方匹配领域冲突，不改变失败阶段。
func (e *CheckoutError) Unwrap() error { return e.Cause }

// CheckoutPorts 只提供运营状态已经合并的设备发现结果。
type CheckoutPorts interface {
	// ResolveScan 在 context 下读取端口编码；错误时结果不能用于创建支付。
	ResolveScan(context.Context, string) (money.ScanResult, error)
}

// CheckoutPricingReader 提供当前有效规则及设备计量兼容检查，不提供管理写入。
type CheckoutPricingReader interface {
	// ActiveDeviceRule 读取站点和设备的当前已发布规则，供首次支付申请保存快照。
	ActiveDeviceRule(context.Context, uint64, string) (charging.Rule, error)
	// CheckDeviceScheme 校验 context 下目标设备能实现给定方案，错误说明不兼容或读取失败。
	CheckDeviceScheme(context.Context, uint64, string, charging.Scheme) error
}

// CheckoutIntents 保存支付申请、端口预占和渠道参数，并读取已有结果供重试。
type CheckoutIntents interface {
	// Replay 按用户、请求、端口、套餐、优惠券编号复用请求；nil 表示尚未创建，异内容返回冲突。
	Replay(context.Context, uint64, string, string, uint64, uint64) (*PaymentIntent, error)
	// Reserve 按输入在完整事务中创建支付标识、套餐及金额快照；失败整体回滚。
	Reserve(context.Context, IntentInput) (PaymentIntent, error)
	// PrepayParams 返回 paymentID 已保存的渠道参数；无参数返回 gorm.ErrRecordNotFound。
	PrepayParams(context.Context, uint64) (PrepayParams, error)
	// SavePrepay 首写 paymentID 的参数；重复保存使用唯一键，错误不得声称已准备付款。
	SavePrepay(context.Context, uint64, PrepayParams) error
}

// CheckoutService 负责报价、支付申请重放、校验与提交后预支付准备。
type CheckoutService struct {
	Ports    CheckoutPorts         // 设备发现能力，cmd 注入，不持有网络配置
	Pricing  CheckoutPricingReader // 有效方案与计量约束
	Intents  CheckoutIntents       // 支付所属请求及预支付 repo
	Provider PrepayProvider        // 通过渠道服务器准备支付参数，不更新订单状态
}

// CheckoutInput 保存客户端请求号与套餐选择，金额由冻结套餐确定，不能由用户填写。
type CheckoutInput struct {
	ClientRequestID string `json:"client_request_id"` // 客户端 UUID，用户范围内唯一
	PortID          string `json:"port_id"`           // 二维码端口业务编码
	OfferID         uint64 `json:"offer_id"`          // 后台发布套餐编号
}

// CheckoutOffers 保留匿名报价响应；不创建支付、订单或用户会话。
type CheckoutOffers struct {
	StationID    uint64           `json:"station_id"`     // 站点 ID
	PortID       string           `json:"port_id"`        // 请求端口编码
	Items        []charging.Offer `json:"items"`          // 当前套餐，金额分、时长分钟、电量按领域单位
	Display      charging.Display `json:"display"`        // 方案展示配置
	StopWhenFull bool             `json:"stop_when_full"` // 充满停止策略
}

// CheckoutResult 保持公开支付准备响应，不包含内部支付主键与 openid。
type CheckoutResult struct {
	IntentID        string       `json:"intent_id"`         // 支付申请 UUID
	MerchantOrderNo string       `json:"merchant_order_no"` // 商户支付单号
	PortID          string       `json:"port_id"`           // 冻结端口编码
	DeviceID        string       `json:"device_id"`         // 冻结设备编号
	StationID       uint64       `json:"station_id"`        // 申请时保存的站点 ID
	ElectricCents   int64        `json:"electric_cents"`    // 预估电费，分
	ServiceCents    int64        `json:"service_cents"`     // 预估服务费，分
	TotalCents      int64        `json:"total_cents"`       // 优惠前总额，分
	DiscountCents   int64        `json:"discount_cents"`    // 已冻结优惠额，分
	PayableCents    int64        `json:"payable_cents"`     // 应付金额，分
	CouponGrantID   uint64       `json:"coupon_grant_id"`   // 优惠券 ID，无券为 0
	PaymentParams   PrepayParams `json:"payment_params"`    // 渠道支付参数，重试复用保存结果
	ExpiresAt       time.Time    `json:"expires_at"`        // 支付期限，UTC
}

// Offers 返回 portID 的当前套餐和展示配置；无需登录，不创建支付或订单。
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

// port 按扫码编码读取端口，区分端口不存在与网关故障；未配置网关时拒绝创建支付。
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

// Start 为用户准备付款：先恢复同请求的支付，再读取当前设备与方案。
// 支付申请提交后才请求预支付参数；失败保留申请供原单号重试，预支付成功不表示到账。
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

// prepare 为 intent 复用或首写预支付参数；ctx 控制 I/O，失败不撤销已提交申请。
// 保存后重读首次参数，避免并发请求返回不同的支付参数。
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
