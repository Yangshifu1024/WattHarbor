package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"net/http"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type StopAuthorization struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a StopAuthorization) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/charge-orders/:order_no/stop-authorization", a.handle)
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
	order, err := payment.FindActiveStopAuthorization(c.Request.Context(), a.DB, c.Param("order_no"), userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not charging for this user", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	if !order.PortID.Valid || order.PortID.Int64 <= 0 {
		httpapi.Write(c, http.StatusConflict, 2000, "port identity unavailable", nil)
		return
	}
	if err := payment.RecordBillingCutoff(c.Request.Context(), a.DB, order.ChargeOrderID, "stop_request", time.Now().UTC()); err != nil {
		httpapi.Write(c, 503, 5001, "计费截止暂时无法保存", nil)
		return
	}
	httpapi.OK(c, gin.H{"charge_order_id": order.ChargeOrderID, "order_no": order.OrderNo,
		"user_id": order.UserID, "device_id": order.DeviceID, "port_no": order.PortNo,
		"port_id": order.PortID.Int64, "start_command_id": order.StartCommandID})
}

type activeStopAuthorizationRow = payment.ActiveStopAuthorization
