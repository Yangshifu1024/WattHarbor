package http

import (
	"context"
	"errors"
	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"net/http"
)

type StartResult = centralcontract.StartResult

type StartResultAPI struct {
	Service interface {
		Apply(context.Context, chargestart.StartResult) (bool, error)
	}
	ServiceToken string
}

func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.StartResultRoute, a.handle)
}

func (a StartResultAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request StartResult
	if err := c.ShouldBindJSON(&request); err != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid start result")
		return
	}
	replayed, err := a.Service.Apply(c.Request.Context(), chargestart.StartResult(request))
	if errors.Is(err, chargestart.ErrStartResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "start result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "start result persistence failed", nil)
		return
	}
	httpapi.OK(c, centralcontract.ResultAcceptance{Accepted: true, Replayed: replayed})
}
