package http

import (
	"context"
	"errors"
	chargestop "github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"net/http"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type StopAuthorization struct {
	Service interface {
		Stop(context.Context, string, uint64) (chargestop.ActiveStopAuthorization, error)
	}
	ServiceToken string
}

func (a StopAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StopAuthorizationRoute, a.handle)
}

func (a StopAuthorization) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil || userID == 0 || c.Param("order_no") == "" {
		httpapi.BadRequest(c, "invalid stop authorization request")
		return
	}
	order, err := a.Service.Stop(c.Request.Context(), c.Param("order_no"), userID)
	if errors.Is(err, chargestop.ErrBillingCutoffUnavailable) {
		httpapi.Write(c, 503, 5001, "计费截止暂时无法保存", nil)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not charging for this user", nil)
		return
	}
	if errors.Is(err, chargestop.ErrPortIdentity) {
		httpapi.Write(c, http.StatusConflict, 2000, "port identity unavailable", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	httpapi.OK(c, centralcontract.StopAuthorizationReply{ChargeOrderID: order.ChargeOrderID, OrderNo: order.OrderNo,
		UserID: order.UserID, DeviceID: order.DeviceID, PortNo: order.PortNo,
		PortID: uint64(order.PortID.Int64), StartCommandID: order.StartCommandID})
}
