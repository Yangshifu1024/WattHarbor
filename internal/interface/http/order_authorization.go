package http

import (
	"context"
	"errors"
	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"net/http"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartAuthorization 是网关启动充电的只读校验入口，仅返回订单和支付记录均已支付的订单。
// 支付状态由支付回调更新。
type StartAuthorization struct {
	Service interface {
		Start(context.Context, string) (chargestart.StartAuthorization, error)
	}
	ServiceToken string
}

func (a StartAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StartAuthorizationRoute, a.handle)
}

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
