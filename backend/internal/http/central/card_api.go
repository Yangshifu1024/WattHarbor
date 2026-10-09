// Package central 提供内部订单授权、设备结果同步、支付确认和任务触发接口。
package central

import (
	"chargepilot/internal/card"
	contract "chargepilot/internal/contracts/central"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
)

// CardAPI 提供内部刷卡和设备卡余额接口。
type CardAPI struct {
	Swipe        card.SwipeService      // 设备刷卡事件完整用例，由 cmd 注入。
	Status       card.CardStatusService // 设备侧卡状态和余额读取，由 cmd 注入。
	ServiceToken string                 // 内部请求令牌，仅在 HTTP 鉴权使用。
}

// RegisterInternal 注册刷卡提交和卡余额查询路由。
func (a CardAPI) RegisterInternal(r *gin.Engine) {
	r.POST(contract.CardSwipePath, a.swipe)
	r.GET(contract.CardBalancePath, a.balance)
}

// internal 校验服务令牌；失败时写入 401，调用方须立即返回。
func (a CardAPI) internal(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// swipe 将事件编号、卡号、端口和发生时间交给刷卡服务。
func (a CardAPI) swipe(c *gin.Context) {
	if !a.internal(c) {
		return
	}
	var in contract.CardSwipeRequest
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "刷卡事件、卡号、端口无效")
		return
	}
	op, err := a.Swipe.Apply(c.Request.Context(), card.SwipeInput{CardNo: in.CardNo, EventID: in.EventID, PortID: in.PortID, OccurredAt: in.OccurredAt})
	if err != nil {
		swipeFailure(c, err)
		return
	}
	httpapi.OK(c, contract.CardOperationReply{OperationID: op.OperationID, DeviceID: op.DeviceID, EventID: op.EventID, ChargeOrderID: op.ChargeOrderID, CardID: op.CardID, Kind: op.Kind, PriceCents: op.PriceCents, Minutes: op.Minutes, Status: op.Status})
}

// swipeFailure 区分参数错误、明确拒绝和结果未知。
// 余额不足仅在明确业务拒绝时返回；结果未知须继续使用原事件编号。
func swipeFailure(c *gin.Context, err error) {
	var failure *card.SwipeError
	if !errors.As(err, &failure) {
		httpapi.Write(c, 503, 5001, "刷卡操作结果暂不可读取，请按同一事件编号查询", nil)
		return
	}
	switch failure.Stage {
	case "input":
		httpapi.BadRequest(c, "刷卡事件、卡号、端口无效")
	case "replay":
		httpapi.Write(c, 503, 5001, "刷卡事件暂不可读取", nil)
	case "expired":
		httpapi.Write(c, 409, 2009, "刷卡事件已过期，不创建新扣款", nil)
	case "port":
		httpapi.Write(c, 409, 2009, "设备离线或端口无效", nil)
	case "paused":
		httpapi.Write(c, 409, 2009, "设备已暂停服务，不接受新的刷卡启动或加时", nil)
	case "capability":
		httpapi.Write(c, 503, 5001, "设备协议能力暂不可读取", nil)
	case "unsupported":
		httpapi.Write(c, 409, 2009, "设备协议不支持在线卡", nil)
	case "session":
		httpapi.Write(c, 503, 5001, "当前刷卡订单暂不可读取", nil)
	case "pricing":
		if errors.Is(err, charging.ErrRuleUnavailable) {
			httpapi.Write(c, 409, 2009, "当前没有可用充电方案", nil)
		} else {
			httpapi.Write(c, 503, 5001, "方案暂不可读取", nil)
		}
	case "compatibility":
		httpapi.Write(c, 409, 2009, err.Error(), nil)
	default:
		if errors.Is(err, card.ErrCardOperation) || errors.Is(err, payment.ErrPaymentIntentConflict) || errors.Is(err, order.ErrChargeNumberConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, card.ErrCardOperation.Error(), gin.H{"insufficient_balance": errors.Is(err, card.ErrCardBalance)})
		} else {
			httpapi.Write(c, 503, 5001, "刷卡操作结果暂不可读取，请按同一事件编号查询", nil)
		}
	}
}
