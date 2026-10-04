package http

import (
	"context"
	"errors"
	chargefinish "github.com/ChargePilot2026/charge-pilot/internal/charging/finish"
	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"net/http"
)

type EndResult struct {
	OrderNo        string         `json:"order_no" binding:"required,max=64"`
	ChargeOrderID  uint64         `json:"charge_order_id" binding:"required"`
	StartCommandID string         `json:"start_command_id" binding:"required,uuid"`
	StopCommandID  string         `json:"stop_command_id" binding:"required,uuid"`
	DeviceID       string         `json:"device_id" binding:"required,max=64"`
	PortNo         uint8          `json:"port_no" binding:"required"`
	PortID         uint64         `json:"port_id" binding:"required"`
	Meter          rules.EndMeter `json:"meter" binding:"required"`
}

type EndResultAPI struct {
	Service interface {
		Apply(context.Context, chargefinish.EndResult) (bool, error)
	}
	ServiceToken string
}

func (a EndResultAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/:order_no/end-result", a.handle)
}

func (a EndResultAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request EndResult
	if c.ShouldBindJSON(&request) != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid end result")
		return
	}
	replayed, err := a.Service.Apply(c.Request.Context(), chargefinish.EndResult(request))
	if errors.Is(err, chargefinish.ErrEndResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "end result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "end result persistence failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true, "replayed": replayed})
}
