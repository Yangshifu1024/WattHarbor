package http

import (
	"context"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

type RefundAPI struct {
	Executor     orderstore.RefundExecutor
	ServiceToken string
}

func (a RefundAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/refunds/dispatch", func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			return
		}
		if a.Executor.Provider == nil {
			httpapi.OK(c, gin.H{"processed": 0, "enabled": false})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		n, err := a.Executor.Batch(ctx)
		if err != nil {
			httpapi.Write(c, 503, 5003, "refund dispatch incomplete; retry scheduled", gin.H{"processed": n})
			return
		}
		httpapi.OK(c, gin.H{"processed": n, "enabled": true})
	})
}
