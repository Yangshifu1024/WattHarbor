package control

import (
	"context"
	"errors"
	"net/http"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/session"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

type Compensation struct {
	Store   store.MySQLSink
	Devices *session.Registry
}

func (c Compensation) Request(ctx context.Context, orderNo, commandID string) error {
	job, needsStop, err := c.Store.CompensateStart(ctx, orderNo, commandID)
	if err != nil || !needsStop {
		return err
	}
	if !c.Devices.Connected(job.DeviceID) {
		return nil
	}
	if err := c.Store.MarkStopSent(ctx, job.CommandID); err != nil {
		return err
	}
	return c.Devices.Send(ctx, job.DeviceID, job.Wire)
}

// RetryStopping 在进程重启或 START 结果不确定后的设备重连时重试停止，不创建第二条 START。
func (c Compensation) RetryStopping(ctx context.Context) error {
	jobs, err := c.Store.StoppingJobs(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, job := range jobs {
		if !c.Devices.Connected(job.DeviceID) {
			continue
		}
		if err := c.Store.MarkStopSent(ctx, job.CommandID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := c.Devices.Send(ctx, job.DeviceID, job.Wire); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type CompensationAPI struct {
	Service interface {
		Request(context.Context, string, string) error
	}
	ServiceToken string
}

func (a CompensationAPI) Register(router *gin.Engine) {
	router.POST(gatewaycontract.CompensateStartRoute, a.handle)
}

func (a CompensationAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var body gatewaycontract.CompensateStartRequest
	if c.Param("order_no") == "" || len(c.Param("order_no")) > 64 || c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid compensation request")
		return
	}
	if err := a.Service.Request(c.Request.Context(), c.Param("order_no"), body.CommandID); err != nil {
		if errors.Is(err, store.ErrOrderConflict) {
			httpapi.Write(c, http.StatusConflict, 2000, "command does not match order", nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "compensation send failed", nil)
		return
	}
	httpapi.OK(c, gatewaycontract.CompensationAcceptance{Accepted: true})
}
