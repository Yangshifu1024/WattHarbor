package http

import (
	"context"
	"errors"
	chargefinish "github.com/ChargePilot2026/charge-pilot/internal/charging/finish"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"net/http"
)

type EndResultAPI struct {
	Service interface {
		Apply(context.Context, chargefinish.EndResult) (bool, error)
	}
	ServiceToken string
}

func (a EndResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.EndResultRoute, a.handle)
}

func (a EndResultAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request centralcontract.EndResultRequest
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
	httpapi.OK(c, centralcontract.ResultReply{Accepted: true, Replayed: replayed})
}
