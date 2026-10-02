package admin

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"time"
)

// auditRow 映射 central_db.audit_log；可空字段使用指针保留 NULL 语义。
type auditRow struct {
	ID         uint64  `json:"id"`          // 审计记录主键
	ActorID    uint64  `json:"actor_id"`    // 操作的后台账号 ID
	ActorName  string  `json:"actor_name"`  // 操作人登录名（当时的快照）
	Module     string  `json:"module"`      // 所属模块，等于目标类型，如 auth / coupon
	Action     string  `json:"action"`      // 动作，如 login / update / grant
	TargetType *string `json:"target_type"` // 目标类型；指针，未指定对象时为 null
	TargetID   *string `json:"target_id"`   // 目标主键（字符串形式）；指针，未指定时为 null
	RequestID  *string `json:"request_id"`  // 关联请求号；指针，取不到时为 null
	ClientIP   *string `json:"client_ip"`   // 客户端 IP；指针，取不到时为 null
	// 快照使用 *string 返回 JSON 文本，避免 []byte 被 encoding/json 编码为 base64。
	BeforeJSON *string   `json:"before_json"` // 变更前快照；指针，新建记录时为 null
	AfterJSON  *string   `json:"after_json"`  // 变更后快照；指针，新建记录时为 null
	CreatedAt  time.Time `json:"created_at"`  // 记录时间
}

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

	// 复用筛选闭包，确保 Count 与列表使用相同过滤条件。
	filtered := func() *gorm.DB {
		q := a.Store.AdminDB.WithContext(c.Request.Context()).Table("audit_log")
		if module != "" {
			q = q.Where("module = ?", module)
		}
		if action != "" {
			q = q.Where("action = ?", action)
		}
		if actor != "" {
			q = q.Where("actor_name LIKE ?", chargehttp.AdminLikePattern(actor))
		}
		if target != "" {
			q = q.Where("target_type = ?", target)
		}
		if from != nil {
			q = q.Where("created_at >= ?", *from)
		}
		if to != nil {
			q = q.Where("created_at <= ?", *to)
		}
		return q
	}
	if err := filtered().Count(&out.Total).Error; err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	var rows []auditRow
	// 将 JSON 列 CAST 为 CHAR 后扫描到字符串，统一快照的文本表示。
	if err := filtered().
		Select("id, actor_id, actor_name, module, action, target_type, target_id, request_id, client_ip, " +
			"CAST(before_json AS CHAR) AS before_json, CAST(after_json AS CHAR) AS after_json, created_at").
		Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Scan(&rows).Error; err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	out.Items = rows
	httpapi.OK(c, out)
}
