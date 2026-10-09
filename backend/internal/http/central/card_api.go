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

// CardAPI 适配内部刷卡及设备余额端点，业务执行由卡服务拥有。
type CardAPI struct {
	Swipe        card.SwipeService      // 设备刷卡事件完整用例，由 cmd 注入。
	Status       card.CardStatusService // 设备侧卡状态和余额读取，由 cmd 注入。
	ServiceToken string                 // 内部请求令牌，仅在 HTTP 鉴权使用。
}

// RegisterInternal 在 r 挂载刷卡 POST 及余额 GET 端点，只注册路由。
func (a CardAPI) RegisterInternal(r *gin.Engine) {
	r.POST(contract.CardSwipePath, a.swipe)
	r.GET(contract.CardBalancePath, a.balance)
}

// internal 从 c 校验服务令牌，成功返回 true；失败写唯一 401 后返回 false，调用方立即返回。
// 不读取卡数据或执行设备命令。
func (a CardAPI) internal(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// swipe 校验 请求中的服务令牌及 JSON，将卡号、事件、端口和时刻交给请求 context 下的刷卡用例。
// 返回操作回执或按阶段映射 400／409／503，事件幂等、扣款和下发由用例拥有。
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

// swipeFailure 将 err 刷卡阶段写入 c400／409／503 响应，不执行 IO 或重试。
// 仅事务业务冲突分支附 insufficient_balance，不把持久失败当作可重新扣款。
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
