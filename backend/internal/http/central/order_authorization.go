package central

import (
	"context"
	"errors"
	"net/http"
	"strings"
	chargestart "wattharbor/internal/charging/start"
	centralcontract "wattharbor/internal/contracts/central"
	"wattharbor/internal/protocol/dc589"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// StartAuthorization 为网关提供订单启动授权，返回编号、模式和额度。
type StartAuthorization struct {
	// Service 核对订单是否允许启动。
	Service interface {
		// Start 读取订单启动授权；订单状态或卡资料无效时返回错误。
		Start(context.Context, string) (chargestart.StartAuthorization, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 注册内部启动授权查询路由。
func (a StartAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StartAuthorizationRoute, a.handle)
}

// handle 校验令牌和订单号，返回授权；卡余额转换为设备使用的 0.1 元单位。
func (a StartAuthorization) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	orderNo := strings.TrimSpace(c.Param("order_no"))
	if len(orderNo) == 0 || len(orderNo) > 64 {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	order, err := a.Service.Start(c.Request.Context(), orderNo)
	if errors.Is(err, chargestart.ErrInvalidCardNumber) {
		httpapi.Write(c, 409, 2000, "冻结卡号无效", nil)
		return
	}
	if errors.Is(err, chargestart.ErrCardSessionUnavailable) {
		httpapi.Write(c, 503, 5001, "刷卡执行快照暂不可读取", nil)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not fully paid", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	balanceUnits := uint16(0)
	if order.ConsumerType == 3 {
		balanceUnits = dc589.CardBalanceUnits(order.WalletAfterCents)
	}
	httpapi.OK(c, centralcontract.StartAuthorizationReply{ConsumerType: order.ConsumerType, CardNumber: order.CardNumber, CardBalanceUnits: balanceUnits,
		ChargeOrderID: order.ChargeOrderID, OrderNo: order.OrderNo, UserID: order.UserID,
		DeviceID: order.DeviceID, PortNo: order.PortNo, PaymentOrderID: order.PaymentOrderID,
		ChargeMode: order.ChargeMode, ChargeQuantity: order.ChargeQuantity,
	})
}
