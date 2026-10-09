package payment

import (
	"errors"

	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DevelopmentAPI 仅在 PAYMENT_MODE=simulation 时注册。
// 模拟确认仍通过支付回调事务，用户和金额取自原支付单。
type DevelopmentAPI struct {
	// Auth 校验当前用户会话及状态，失败已写唯一响应。
	Auth    authhttp.SessionAuthenticator
	Service payment.SimulationService // 仅 simulation 模式装配的确认用例
}

// Register 注册当前用户的模拟支付确认路由。
func (a DevelopmentAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/development/payments/confirm", a.confirm)
}

// confirm 核对当前用户身份及商户订单号，确认该用户的模拟支付。
func (a DevelopmentAPI) confirm(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// MerchantOrderNo 当前用户的模拟支付商户订单号。
		MerchantOrderNo string `json:"merchant_order_no" binding:"required,max=64"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "invalid payment request")
		return
	}
	result, err := a.Service.ConfirmUser(c.Request.Context(), user, in.MerchantOrderNo)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "payment not found", nil)
		return
	}
	if errors.Is(err, payment.ErrSimulationLookup) {
		httpapi.Write(c, 503, 5001, "payment unavailable", nil)
		return
	}
	if errors.Is(err, payment.ErrNotSimulationCheckout) {
		httpapi.Write(c, 409, 3001, "payment is not a simulated checkout", nil)
		return
	}

	if errors.Is(err, payment.ErrPaymentCallbackConflict) {
		httpapi.Write(c, 409, 3001, "simulation payment conflict", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "simulation confirmation unavailable", nil)
		return
	}
	httpapi.OK(c, result)
}
