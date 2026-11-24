package ops

import (
	"time"

	"github.com/gin-gonic/gin"
	adminhttp "wattharbor/internal/http/admin"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// registerAuditLogs 挂载审计日志查询接口，权限为 audit.read。
func (a API) registerAuditLogs(r *gin.Engine) {
	r.GET("/api/v1/admin/audit-logs", a.Auth.Require("audit.read"), a.listAuditLogs)
}

// listAuditLogs 按模块、动作、操作人、目标类型及时间分页查询审计记录，按 ID 倒序。
// 时间参数必须符合 RFC3339；分页或时间参数无效时返回 400。
func (a API) listAuditLogs(c *gin.Context) {
	page, ok := adminhttp.ReadAdminPage(c, "")
	if !ok {
		return
	}

	// 仅解析 RFC3339 输入；存储过滤和分页计数由审计仓储负责。
	var (
		module, action, actor, target string
		from, to                      *time.Time
		badTime                       bool
	)
	module, action, actor, target = c.Query("module"), c.Query("action"), c.Query("actor"), c.Query("target_type")
	for _, spec := range []struct {
		raw  string      // 查询参数
		into **time.Time // 对应可空 UTC 时刻
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

	out, err := a.Audits.List(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor, ops.AuditQuery{
		Module: module, Action: action, Actor: actor, TargetType: target, From: from, To: to,
	}, page)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}
