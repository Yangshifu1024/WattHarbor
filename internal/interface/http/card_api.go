package http

import (
	"errors"
	cardstore "github.com/ChargePilot2026/charge-pilot/internal/card"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"net/http"
	"regexp"
	"strings"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type CardAPI struct {
	Store        cardstore.CardStore
	Auth         SessionAuthenticator
	Pricing      pricing.PricingStore
	Scan         money.PortLookup
	ServiceToken string
}

// RegisterUser 挂载 C 端在线卡接口。这些路由归 user 进程，鉴权用用户访问令牌。
func (a CardAPI) RegisterUser(r *gin.Engine) {
	r.GET("/api/v1/user/cards", a.cards)
	r.POST("/api/v1/user/cards/:card_no/status", a.cardStatus)
}

// RegisterInternal 挂载设备侧刷卡的内部接口。这些路由归 central 进程，
// 由 gateway 携服务令牌调用，用户令牌不适用。
func (a CardAPI) RegisterInternal(r *gin.Engine) {
	r.POST("/api/v1/internal/cards/swipe", a.swipe)
	r.GET("/api/v1/internal/cards/balance", a.balance)
}
func (a CardAPI) internal(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}
func (a CardAPI) cards(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	rows := []cardstore.OnlineCard{}
	if err := a.Store.DB.WithContext(c.Request.Context()).Where("user_id=? AND status<>'unbound'", user).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5001, "卡信息暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// Binding is performed by an authorized operator after verifying ownership.
// Merely knowing a printed card number cannot claim an existing user's card.
func (a CardAPI) cardStatus(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Status != "lost" && in.Status != "unbound" {
		httpapi.BadRequest(c, "仅可挂失或解绑自己的卡")
		return
	}
	err := a.Store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		card, err := (cardstore.UserCardStore{DB: a.Store.DB}).LockUserCard(tx, c.Param("card_no"), user)
		if err != nil {
			return err
		}
		return (cardstore.UserCardStore{DB: a.Store.DB}).SetUserCardStatus(tx, card, user, in.Status)
	})
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
func (a CardAPI) swipe(c *gin.Context) {
	if !a.internal(c) {
		return
	}
	var in struct {
		CardNo     string    `json:"card_no"`
		EventID    string    `json:"event_id"`
		PortID     string    `json:"port_id"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	if c.ShouldBindJSON(&in) != nil || !portCodePattern.MatchString(in.PortID) || strings.TrimSpace(in.CardNo) == "" || len(in.CardNo) > 64 || len(in.EventID) > 128 || strings.TrimSpace(in.EventID) == "" {
		httpapi.BadRequest(c, "刷卡事件、卡号、端口无效")
		return
	}
	prior, replayed, err := (cardstore.UserCardStore{DB: a.Store.DB}).FindCardOperationByEvent(
		c.Request.Context(), a.Store.DB, in.EventID, in.CardNo, in.PortID)
	if err != nil {
		httpapi.Write(c, 503, 5001, "刷卡事件暂不可读取", nil)
		return
	}
	if replayed {
		httpapi.OK(c, prior)
		return
	}
	if in.OccurredAt.IsZero() || time.Since(in.OccurredAt) > 30*time.Second || in.OccurredAt.After(time.Now().Add(time.Second)) {
		httpapi.Write(c, 409, 2009, "刷卡事件已过期，不创建新扣款", nil)
		return
	}
	port, status := a.Scan.LookupPort(c.Request.Context(), in.PortID)
	if status != http.StatusOK || !port.Found || !port.Online {
		httpapi.Write(c, 409, 2009, "设备离线或端口无效", nil)
		return
	}
	if port.DeviceStatus != "enabled" {
		httpapi.Write(c, 409, 2009, "设备已暂停服务，不接受新的刷卡启动或加时", nil)
		return
	}
	_, cap, err := a.Pricing.DeviceCapabilities(c.Request.Context(), port.StationID, port.DeviceID)
	if err != nil {
		httpapi.Write(c, 503, 5001, "设备协议能力暂不可读取", nil)
		return
	}
	if !cap.OnlineCard {
		httpapi.Write(c, 409, 2009, "设备协议不支持在线卡", nil)
		return
	}
	var active int64
	if err := a.Store.DB.WithContext(c.Request.Context()).Model(&cardstore.CardCharge{}).Where("active_port=?", in.PortID).Count(&active).Error; err != nil {
		httpapi.Write(c, 503, 5001, "当前刷卡订单暂不可读取", nil)
		return
	}
	// An extension uses its frozen package even if the current scheme changed.
	// The transaction rechecks the session before any debit.
	var rule charging.Rule
	if active == 0 {
		var err error
		rule, err = a.Pricing.ActiveDeviceRule(c.Request.Context(), port.StationID, port.DeviceID)
		if err != nil {
			if errors.Is(err, charging.ErrRuleUnavailable) {
				httpapi.Write(c, 409, 2009, "当前没有可用充电方案", nil)
			} else {
				httpapi.Write(c, 503, 5001, "方案暂不可读取", nil)
			}
			return
		}
	}
	if active == 0 && rule.Spec.Scheme != nil {
		if err := a.Pricing.CheckDeviceScheme(c.Request.Context(), port.StationID, port.DeviceID, *rule.Spec.Scheme); err != nil {
			httpapi.Write(c, 409, 2009, err.Error(), nil)
			return
		}
	}
	op, err := a.Store.Swipe(c.Request.Context(), in.CardNo, in.EventID, port, rule)
	if err != nil {
		if errors.Is(err, cardstore.ErrCardOperation) || errors.Is(err, payment.ErrPaymentIntentConflict) || errors.Is(err, orderstore.ErrChargeNumberConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, cardstore.ErrCardOperation.Error(), gin.H{"insufficient_balance": errors.Is(err, cardstore.ErrCardBalance)})
		} else {
			httpapi.Write(c, 503, 5001, "刷卡操作结果暂不可读取，请按同一事件编号查询", nil)
		}
		return
	}
	httpapi.OK(c, op)
}

// portCodePattern 校验刷卡请求中的端口编码字符集与长度。
// 与 scan 家族各自维护输入契约，故不与领域层共享。
var portCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)
