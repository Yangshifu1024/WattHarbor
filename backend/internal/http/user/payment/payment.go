// Package payment 提供匿名扫码报价、登录支付和支付通知接口。
package payment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	charging "wattharbor/internal/domain/charging"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/marketing"
	"wattharbor/internal/payment"
	"wattharbor/internal/platform/httpapi"
)

// StartAPI 提供匿名报价和登录后的支付申请接口。
type StartAPI struct {
	Auth     authhttp.SessionAuthenticator // 当前用户身份适配
	Checkout payment.CheckoutService       // 匿名报价及登录支付准备用例
}

// Register 注册匿名报价和登录支付路由。
func (a StartAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/offers", a.offers)
	router.POST("/api/v1/user/scan/start", a.start)
}

// offers 返回指定端口当前可购买的充电方案。
func (a StartAPI) offers(c *gin.Context) {
	var body struct {
		PortID string `json:"port_id"` /* 二维码端口编码 */
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

// start 读取当前用户身份并创建支付申请，金额和方案由支付服务核对。
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

// checkoutFailure 按失败阶段返回公开支付提示，隐藏存储原因和渠道敏感信息。
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

// NotificationVerifier 验证支付通知签名并解密订单信息。
type NotificationVerifier interface {
	// VerifyNotification 验证渠道签名并解密通知；失败时不得确认到账。
	VerifyNotification(context.Context, *http.Request) (payment.VerifiedTransaction, error)
}

// WechatCallbackAPI 先验证微信支付通知，再交给支付服务确认到账。
type WechatCallbackAPI struct {
	Verifier  NotificationVerifier    // cmd 注入的签名校验与解密能力
	Callbacks payment.CallbackService // 已验签通知的资金用例，不接受客户端自报到账
}

// Register 注册公开微信支付通知路由；渠道签名用于确认来源。
func (a WechatCallbackAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/public/payments/wechat/callback", a.callback)
}

// callback 先验签再确认到账；成功或同一通知重复提交均返回 204。
// 验签失败返回 400，订单冲突返回 409，其余失败返回 503。
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
