package http

import (
	"errors"

	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DevelopmentPaymentAPI 仅在 PAYMENT_MODE=simulation 时注册。
// 通过正常支付回调事务确认当前用户的模拟结算，不接受任意金额或用户身份。
type DevelopmentPaymentAPI struct {
	Auth    authhttp.SessionAuthenticator
	Service payment.SimulationService // 仅 simulation 模式装配的确认用例
}

func (a DevelopmentPaymentAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/development/payments/confirm", a.confirm)
}
func (a DevelopmentPaymentAPI) confirm(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
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
