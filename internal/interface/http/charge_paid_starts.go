package http

import (
	"context"
	"net/http"
	"strconv"

	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

type PaidStartsAPI struct {
	Service interface {
		PaidStarts(context.Context, uint64) ([]chargestart.PaidChargeOrder, error)
	}
	ServiceToken string
}

func (a PaidStartsAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/internal/charge-orders/paid-starts", a.list)
}

func (a PaidStartsAPI) list(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	after, err := strconv.ParseUint(c.DefaultQuery("after_id", "0"), 10, 64)
	if err != nil {
		httpapi.BadRequest(c, "invalid paid start cursor")
		return
	}
	items, err := a.Service.PaidStarts(c.Request.Context(), after)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "paid orders unavailable", nil)
		return
	}
	if items == nil {
		items = []chargestart.PaidChargeOrder{}
	}
	httpapi.OK(c, gin.H{"items": items})
}
