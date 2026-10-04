package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

type UserStopAPI struct {
	JWT      *auth.JWT
	Sessions interface {
		Exists(context.Context, string) (bool, error)
	}
	Users interface {
		Active(context.Context, uint64) (bool, error)
	}
	Gateway interface {
		RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error)
	}
}

func (a UserStopAPI) Register(router *gin.Engine) { router.POST("/api/v1/user/charge/stop", a.handle) }

func (a UserStopAPI) handle(c *gin.Context) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	claims, err := a.JWT.Verify(parts[1], "user", time.Now())
	if err != nil || claims.SessionID == "" {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	valid, err := a.Sessions.Exists(c.Request.Context(), claims.SessionID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session unavailable", nil)
		return
	}
	if !valid {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "session expired", nil)
		return
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID == 0 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	active, err := a.Users.Active(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user unavailable", nil)
		return
	}
	if !active {
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return
	}
	var body struct {
		OrderNo string `json:"order_no" binding:"required,max=64"`
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	if a.Gateway == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway configuration invalid", nil)
		return
	}
	acceptance, err := a.Gateway.RequestStop(c.Request.Context(), gatewaycontract.StopRequest{OrderNo: body.OrderNo, UserID: userID, Source: gatewaycontract.StopSourceUser})
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && rejection.StatusCode == http.StatusConflict {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not stoppable", nil)
		return
	}
	if errors.Is(err, gatewaycontract.ErrInvalidResponse) {
		httpapi.Write(c, http.StatusBadGateway, 5003, "invalid gateway response", nil)
		return
	}
	var businessError gatewaycontract.BusinessError
	if rejection != nil || errors.As(err, &businessError) {
		httpapi.Write(c, http.StatusBadGateway, 5003, "gateway stop not accepted", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway unavailable", nil)
		return
	}
	httpapi.OK(c, acceptance)
}
