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

// SimulationCallbackAPI 适配内部模拟支付成功通知，原意图身份及到账事务由payment用例拥有。
// 仅simulation部署模式由cmd注册，不接受客户端金额或用户身份。
type SimulationCallbackAPI struct {
	Service      payment.SimulationService // 原意图读取与模拟回调用例
	ServiceToken string                    // 内部模拟通知的服务令牌，仅由适配器验证
}

// Register 在router挂载原模拟成功POST路径，无返回值或业务IO；部署模式由cmd选择。
func (a SimulationCallbackAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/payments/simulate-success", a.callback)
}

// callback 核对c服务令牌和商户订单号，以请求context确认原模拟支付并返回原回执。
// 坏输入400、意图缺失404、支付冲突409、读取或持久失败503，不在HTTP执行资金事务。
func (a SimulationCallbackAPI) callback(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var body struct {
		MerchantOrderNo string `json:"merchant_order_no" binding:"required,max=64"` // 原模拟渠道支付单号
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
