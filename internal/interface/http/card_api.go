package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
)

// CardAPI 适配用户卡和内部刷卡端点；业务准入、查询及完整事务由注入用例拥有。
type CardAPI struct {
	Swipe        card.SwipeService      // 设备事件用例；central 注册内部端点时注入
	Status       card.CardStatusService // 所属卡状态及设备余额读取
	Auth         SessionAuthenticator   // 用户会话适配；内部路由不使用
	ServiceToken string                 // 内部端点鉴权值，不传入业务用例
}

// RegisterUser 在 r 注册原用户列表与状态端点；无业务 IO 或返回值。
func (a CardAPI) RegisterUser(r *gin.Engine) {
	r.GET("/api/v1/user/cards", a.cards)
	r.POST("/api/v1/user/cards/:card_no/status", a.cardStatus)
}

// RegisterInternal 在 r 注册原刷卡与余额端点；无业务 IO 或返回值。
func (a CardAPI) RegisterInternal(r *gin.Engine) {
	r.POST(contract.CardSwipePath, a.swipe)
	r.GET(contract.CardBalancePath, a.balance)
}

// internal 从 c 校验服务令牌，返回准入标志；失败写 401，不读取业务数据。
func (a CardAPI) internal(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// cards 从 c 验证用户身份，调用所属卡列表用例并写原 items 或 503 响应。
func (a CardAPI) cards(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	rows, err := a.Status.List(c.Request.Context(), user)
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡信息暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// cardStatus 从 c 读取用户、卡号及状态，调用原子状态用例并映射输入／缺卡／存储错误。
func (a CardAPI) cardStatus(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct{ Status string }
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "仅可挂失或解绑自己的卡")
		return
	}
	err := a.Status.Change(c.Request.Context(), user, c.Param("card_no"), in.Status)
	if errors.Is(err, card.ErrCardStatusInput) {
		httpapi.BadRequest(c, "仅可挂失或解绑自己的卡")
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "卡不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡状态暂不可保存", nil)
		return
	}
	httpapi.OK(c, gin.H{"status": in.Status})
}

// swipe 从 c 校验服务身份及 JSON，以原事件调用用例并写原操作或公开错误；不访问数据库。
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

// swipeFailure 将 err 阶段映射为原 c 响应；不足余额字段只在原事务业务冲突分支输出。
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
