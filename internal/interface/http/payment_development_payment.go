package http

import (
	"encoding/json"
	"errors"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	paystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DevelopmentPaymentAPI 仅在 PAYMENT_MODE=simulation 时注册。
// 通过正常支付回调事务确认当前用户的模拟结算，不接受任意金额或用户身份。
type DevelopmentPaymentAPI struct {
	Auth  SessionAuthenticator
	DB    *gorm.DB
	Store paystore.PaymentCallbackStore
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
	order, err := orderstore.FindDevelopmentPayment(c.Request.Context(), a.DB, in.MerchantOrderNo, user)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "payment not found", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "payment unavailable", nil)
		return
	}
	var prepay orderstore.ChargePrepayRecord
	var params struct {
		Provider string `json:"provider"`
	}
	if err = a.DB.WithContext(c.Request.Context()).Where("payment_order_id=?", order.ID).Take(&prepay).Error; err != nil || json.Unmarshal(prepay.ParamsJSON, &params) != nil || params.Provider != "simulation" {
		httpapi.Write(c, 409, 3001, "payment is not a simulated checkout", nil)
		return
	}
	// Stable provider receipt values make concurrent/repeated confirmations use
	// exactly the same callback digest rather than generating a second payment.
	result, err := a.Store.Apply(c.Request.Context(), paystore.VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation", AppID: a.Store.ExpectedAppID, MerchantOrderNo: order.OrderNo, TransactionID: "SIMH5" + order.OrderNo, OpenID: order.OpenID, PaidCents: order.TotalCents, PaidAt: order.CreatedAt})
	if errors.Is(err, paystore.ErrPaymentCallbackConflict) {
		httpapi.Write(c, 409, 3001, "simulation payment conflict", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "simulation confirmation unavailable", nil)
		return
	}
	httpapi.OK(c, result)
}
