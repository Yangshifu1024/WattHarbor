package ops

import (
	"context"
	"net/http"
	"time"

	"chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	adminops "chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const DiagnosticsPath = "/api/v1/internal/diagnostics"

// DiagnosticsAPI 汇总中央、网关和 worker 的诊断，所有读端独立报告可用性。
type DiagnosticsAPI struct {
	DB         *gorm.DB
	Auth       authhttp.AdminAuth
	Transport  serviceclient.Client
	Token      string
	GatewayURL string
	WorkerURL  string
}

func (a DiagnosticsAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/diagnostics", a.Auth.Require("audit.read"), a.read)
}

func (a DiagnosticsAPI) read(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	audit := admin.AdminAuditContext(c)
	if err := adminops.AppendAuditInTx(a.DB.WithContext(ctx), audit, adminops.AuditChange{
		Action: "read", Target: "diagnostics",
	}); err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}

	central := adminops.CentralDiagnostics(ctx, a.DB)
	unavailable := []string{}
	tasks := central.Tasks
	queues := central.Queues
	unavailable = append(unavailable, central.Unavailable...)
	for _, source := range []struct{ name, url string }{{"gateway", a.GatewayURL}, {"worker", a.WorkerURL}} {
		var wire struct {
			Data *adminops.DiagnosticsReply `json:"data"`
		}
		if source.url == "" || a.Transport.GetJSON(ctx, source.url, a.Token, DiagnosticsPath, &wire) != nil || wire.Data == nil {
			unavailable = append(unavailable, source.name)
			continue
		}
		for name, entry := range wire.Data.Tasks {
			tasks[source.name+"."+name] = entry
		}
		for name, entry := range wire.Data.Queues {
			queues[name] = entry
		}
		unavailable = append(unavailable, wire.Data.Unavailable...)
	}
	httpapi.OK(c, gin.H{"scanners": tasks, "queues": queues, "unavailable": unavailable})
}

// RegisterInternalDiagnostics 提供仅供后台汇总的进程本地快照。
func RegisterInternalDiagnostics(r *gin.Engine, token string, snapshot func(context.Context) adminops.DiagnosticsReply) {
	r.GET(DiagnosticsPath, authhttp.RequireServiceToken(token), func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "诊断暂不可用", nil)
			return
		}
		httpapi.OK(c, snapshot(ctx))
	})
}
