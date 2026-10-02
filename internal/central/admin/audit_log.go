package admin

import (
	paystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"time"
)

// auditRow 是审计列表的响应行，定义在持久化层。
type auditRow = paystore.AuditRow

// registerAuditLogs 挂载审计日志查询接口，权限为 audit.read。
func (a ResourceAPI) registerAuditLogs(r *gin.Engine) {
	r.GET("/api/v1/admin/audit-logs", a.Auth.Require("audit.read"), a.listAuditLogs)
}

// listAuditLogs 按模块、动作、操作人、目标类型及时间分页查询审计记录，按 ID 倒序。
// 时间参数必须符合 RFC3339；分页或时间参数无效时返回 400。
func (a ResourceAPI) listAuditLogs(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	out := Page[auditRow]{Items: []auditRow{}, Page: page.Page, PageSize: page.PageSize}

	// Count 与列表查询分别构造 GORM 语句，避免计数操作修改后续查询的投影。
	var (
		module, action, actor, target string
		from, to                      *time.Time
		badTime                       bool
	)
	module, action, actor, target = c.Query("module"), c.Query("action"), c.Query("actor"), c.Query("target_type")
	for _, spec := range []struct {
		raw  string
		into **time.Time
	}{{c.Query("from"), &from}, {c.Query("to"), &to}} {
		if spec.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, spec.raw)
		if err != nil {
			badTime = true
			break
		}
		utc := parsed.UTC()
		*spec.into = &utc
	}
	if badTime {
		httpapi.BadRequest(c, "时间须为 RFC 3339 格式")
		return
	}

	out, err := paystore.ListAudits(c.Request.Context(), a.Store.AdminDB, paystore.AuditQuery{
		Module: module, Action: action, Actor: actor, TargetType: target, From: from, To: to,
	}, page.Page, page.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}
