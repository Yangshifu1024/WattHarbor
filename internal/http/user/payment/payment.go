package payment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// StartAPI 只绑定支付申请、提取用户身份并映射用例结果。
type StartAPI struct {
	Auth     authhttp.SessionAuthenticator // 当前用户身份适配
	Checkout payment.CheckoutService       // 匿名报价及登录支付准备用例
}

// Register 在 router 上保持匿名报价与登录创建支付的原 URL。
func (a StartAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/offers", a.offers)
	router.POST("/api/v1/user/scan/start", a.start)
}

// offers 绑定 c 的匿名端口报价请求，业务拒绝由 Checkout 分类后映射。
func (a StartAPI) offers(c *gin.Context) {
	var body struct {
		PortID string `json:"port_id"` /* 原二维码端口编码 */
	}
	if c.ShouldBindJSON(&body) != nil || !userScanCodePattern.MatchString(body.PortID) {
		httpapi.BadRequest(c, "端口编码无效")
		return
	}
	out, err := a.Checkout.Offers(c.Request.Context(), body.PortID)
	if err != nil {
		var failure *payment.CheckoutError
		if errors.As(err, &failure) {
			switch failure.Kind {
			case payment.CheckoutNotFound:
				httpapi.Write(c, 404, 1004, "端口不存在", nil)
				return
			case payment.CheckoutGateway:
				httpapi.Write(c, 503, 5003, "设备信息暂不可读取，请稍后重试", nil)
				return
			case payment.CheckoutPaused:
				httpapi.Write(c, 409, 2001, "设备已暂停服务，请选择其他设备", nil)
				return
			case payment.CheckoutCompatibility:
				httpapi.Write(c, 409, 2009, err.Error(), nil)
				return
			}
		}
		httpapi.Write(c, 503, 5003, "充电方案暂时无法读取", nil)
		return
	}
	httpapi.OK(c, out)
}

// start 提取 c 的登录身份并严格绑定原请求，调用用例后返回原公开形状。
func (a StartAPI) start(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	if a.Checkout.Provider == nil {
		httpapi.Write(c, 503, 5003, "payment provider unavailable", nil)
		return
	}
	var in payment.CheckoutInput
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || uuid.Validate(in.ClientRequestID) != nil || !userScanCodePattern.MatchString(in.PortID) {
		httpapi.BadRequest(c, "invalid payment request")
		return
	}
	if in.OfferID == 0 {
		httpapi.BadRequest(c, "请选择后台发布的充电方案")
		return
	}
	out, err := a.Checkout.Start(c.Request.Context(), userID, in)
	if err != nil {
		checkoutFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// checkoutFailure 将 err 的业务阶段及领域错误映射到原支付状态、code 和文案。
// c 的错误响应不包含存储错误正文或渠道敏感信息。
func checkoutFailure(c *gin.Context, err error) {
	var failure *payment.CheckoutError
	if errors.As(err, &failure) {
		switch failure.Kind {
		case payment.CheckoutProvider:
			httpapi.Write(c, 503, 5003, "payment provider unavailable", nil)
			return
		case payment.CheckoutNotFound:
			httpapi.Write(c, 404, 1004, "port not found", nil)
			return
		case payment.CheckoutGateway:
			httpapi.Write(c, 503, 5003, "gateway unavailable", nil)
			return
		case payment.CheckoutPaused:
			httpapi.Write(c, 409, 2001, "设备已暂停服务，请选择其他设备", nil)
			return
		case payment.CheckoutPort:
			httpapi.Write(c, 409, 2001, "port unavailable", nil)
			return
		case payment.CheckoutPricing:
			httpapi.Write(c, 409, 2004, "pricing unavailable", nil)
			return
		case payment.CheckoutPricingStorage:
			httpapi.Write(c, 503, 5003, "pricing storage unavailable", nil)
			return
		case payment.CheckoutOffer:
			httpapi.Write(c, 409, 2004, "充电方案已下架", nil)
			return
		case payment.CheckoutCompatibility:
			httpapi.Write(c, 409, 2009, err.Error(), nil)
			return
		case payment.CheckoutPrepay:
			httpapi.Write(c, 503, 3001, "payment prepay unavailable", nil)
			return
		case payment.CheckoutInvalidInput:
			httpapi.BadRequest(c, "invalid payment request")
			return
		}
	}
	switch {
	case errors.Is(err, charging.ErrInvalidPricing):
		httpapi.BadRequest(c, "invalid charging estimate")
	case errors.Is(err, marketing.ErrCouponNotFound):
		httpapi.BadRequest(c, "优惠券不存在或未发放给该账号")
	case errors.Is(err, marketing.ErrCouponExhausted):
		httpapi.Write(c, 409, 2001, "优惠券已过期或已使用", nil)
	case errors.Is(err, marketing.ErrCouponThreshold):
		httpapi.BadRequest(c, "未达到该优惠券的使用门槛")
	case errors.Is(err, payment.ErrPaymentIntentConflict):
		httpapi.Write(c, 409, 2001, "payment intent unavailable", nil)
	default:
		httpapi.Write(c, 503, 5001, "payment storage unavailable", nil)
	}
}

// NotificationVerifier 是公开渠道通知验签和解密的窄能力，HTTP 只接受已验证的到账身份。
type NotificationVerifier interface {
	// VerifyNotification 在 context 下验证原 HTTP 请求的渠道签名并解密，返回已验证的到账身份或错误；不写资金，失败不得调用回调用例。
	VerifyNotification(context.Context, *http.Request) (payment.VerifiedTransaction, error)
}

// WechatCallbackAPI 将公开微信通知先验签再交给所属支付回调用例，资金及幂等事务归用例执行。
type WechatCallbackAPI struct {
	Verifier  NotificationVerifier    // cmd 注入的签名校验与解密能力
	Callbacks payment.CallbackService // 已验签通知的资金用例，不接受客户端自报到账
}

// Register 在 router 上保持原公开渠道通知端点；通知不使用用户会话。
func (a WechatCallbackAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/public/payments/wechat/callback", a.callback)
}

// callback 用 c 的原请求验签，再调用唯一资金入口；成功及相同通知重放均返回 204。
// 验签失败为 400，资金身份冲突为 409，依赖或持久化故障为 503，不泄露渠道通知正文。
func (a WechatCallbackAPI) callback(c *gin.Context) {
	if a.Verifier == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "payment callback unavailable", nil)
		return
	}
	transaction, err := a.Verifier.VerifyNotification(c.Request.Context(), c.Request)
	if err != nil {
		httpapi.Write(c, http.StatusBadRequest, 3001, "invalid payment notification", nil)
		return
	}
	_, err = a.Callbacks.Apply(c.Request.Context(), payment.VerifiedPayment{Provider: transaction.Provider, MerchantID: transaction.MerchantID,
		AppID: transaction.AppID, MerchantOrderNo: transaction.MerchantOrderNo, TransactionID: transaction.TransactionID,
		OpenID: transaction.OpenID, PaidCents: transaction.PaidCents, PaidAt: transaction.PaidAt})
	if errors.Is(err, payment.ErrPaymentCallbackConflict) {
		httpapi.Write(c, http.StatusConflict, 3001, "payment does not match intent", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "payment persistence unavailable", nil)
		return
	}
	c.Status(http.StatusNoContent)
}
