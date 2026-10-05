package central

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

// StartAuthorization 适配网关按原订单号读取起充授权，支付／在线卡资格由所属用例核对。
// 只返回原执行身份、模式及数量，不发起起充命令或更新支付状态。
type StartAuthorization struct {
	// Service 是cmd注入的起充授权窄能力，不暴露数据库或通用HTTP。
	Service interface {
		// Start 以请求context及原订单号读取授权，返回执行事实或资格／快照／存储错误，不下发命令。
		Start(context.Context, string) (chargestart.StartAuthorization, error)
	}
	ServiceToken string // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 将原起充授权GET路径挂到router，无返回值或业务IO。
func (a StartAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StartAuthorizationRoute, a.handle)
}

// handle 从c校验服务令牌及原订单号，调用所属授权读取，输出原整数身份和DC589卡余额单位。
// 坏输入400、订单状态409、存储或卡快照503；写一次响应后返回，不持有业务事务。
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
