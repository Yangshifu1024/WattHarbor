package central

import (
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
)

// SimulationCallbackAPI 接收模拟支付成功通知，仅在 simulation 模式注册。
// 金额和用户取自支付订单。
type SimulationCallbackAPI struct {
	Service      payment.SimulationService // 意图读取与模拟回调用例
	ServiceToken string                    // 内部模拟通知的服务令牌，仅由适配器验证
}

// Register 注册内部模拟支付确认路由。
func (a SimulationCallbackAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/payments/simulate-success", a.callback)
}

// callback 校验服务令牌和商户订单号，通过支付服务确认模拟到账。
func (a SimulationCallbackAPI) callback(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var body struct {
		MerchantOrderNo string `json:"merchant_order_no" binding:"required,max=64"` // 模拟渠道支付单号
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid simulation request")
		return
	}
	result, err := a.Service.ConfirmIntent(c.Request.Context(), body.MerchantOrderNo)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "payment intent not found", nil)
		return
	}

	if errors.Is(err, payment.ErrSimulationLookup) {
		httpapi.Write(c, 503, 5001, "payment intent unavailable", nil)
		return
	}
	if errors.Is(err, payment.ErrPaymentCallbackConflict) {
		httpapi.Write(c, http.StatusConflict, 3001, "simulation payment conflict", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "simulation persistence unavailable", nil)
		return
	}
	httpapi.OK(c, result)
}
