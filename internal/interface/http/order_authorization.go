package http

import (
	"errors"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"net/http"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartAuthorization 是网关启动充电的只读校验入口，仅返回订单和支付记录均已支付的订单。
// 支付状态由支付回调更新。
type StartAuthorization struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a StartAuthorization) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/charge-orders/:order_no/start-authorization", a.handle)
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
	order, err := orderstore.FindPaidStartAuthorization(c.Request.Context(), a.DB, orderNo)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not fully paid", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	consumer, cardNumber, balanceUnits := uint8(2), uint32(0), uint16(0)
	cardSession, err := orderstore.FindCardChargeSession(c.Request.Context(), a.DB, order.ChargeOrderID)
	if err != nil {
		httpapi.Write(c, 503, 5001, "刷卡执行快照暂不可读取", nil)
		return
	}
	if cardSession.ChargeOrderID != 0 {
		n, err := strconv.ParseUint(cardSession.CardNo, 10, 32)
		if err != nil || n == 0 {
			httpapi.Write(c, 409, 2000, "冻结卡号无效", nil)
			return
		}
		consumer = 3
		cardNumber = uint32(n)
		balanceUnits = dc589.CardBalanceUnits(cardSession.WalletAfterCents)
	}
	httpapi.OK(c, gin.H{"consumer_type": consumer, "card_number": cardNumber, "card_balance_units": balanceUnits,
		"charge_order_id":  order.ChargeOrderID,
		"order_no":         order.OrderNo,
		"user_id":          order.UserID,
		"device_id":        order.DeviceID,
		"port_no":          order.PortNo,
		"payment_order_id": order.PaymentOrderID,
		"charge_mode":      order.ChargeMode,
		"charge_quantity":  order.ChargeQuantity,
	})
}

type paidStartAuthorizationRow = orderstore.PaidStartAuthorization
