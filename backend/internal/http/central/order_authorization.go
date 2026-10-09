package central

import (
	chargestart "chargepilot/internal/charging/start"
	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/protocol/dc589"
	"context"
	"errors"
	"net/http"
	"strings"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartAuthorization 适配网关按订单号读取起充授权，支付／在线卡资格由用例核对。
// 只返回执行所需编号、模式及数量，不发起起充命令或更新支付状态。
type StartAuthorization struct {
	// Service 是 cmd 注入的起充授权接口，不暴露数据库或通用 HTTP。
	Service interface {
		// Start 以请求 context 及订单号读取授权，返回执行事实或资格／快照／存储错误，不下发命令。
		Start(context.Context, string) (chargestart.StartAuthorization, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 将起充授权 GET 路径挂到 router，只注册路由。
func (a StartAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StartAuthorizationRoute, a.handle)
}

// handle 从 c 校验服务令牌及订单号，调用授权读取，输出整数编号和 DC589 卡余额单位。
// 坏输入 400、订单状态 409、存储或卡快照 503；写一次响应后返回，不持有业务事务。
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
