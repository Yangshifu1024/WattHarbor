package control

import (
	"context"
	"net/http"
	"strings"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

type StartAPI struct {
	Service interface {
		Start(context.Context, string) (store.StartReservation, error)
	}
	ServiceToken string
}

func (a StartAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.StartPath, a.start)
}

func (a StartAPI) start(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request gatewaycontract.StartRequest
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.OrderNo) != request.OrderNo {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	command, err := a.Service.Start(c.Request.Context(), request.OrderNo)
	if err != nil {
		httpapi.Write(c, startStatusCode(err), 2000, "charge start unavailable", nil)
		return
	}
	httpapi.Write(c, http.StatusAccepted, 0, "accepted", gatewaycontract.StartAcceptance{
		OrderNo: command.OrderNo, CommandID: command.CommandID, Status: command.Status,
		DeviceAcknowledged: command.Status == "acked",
	})
}
