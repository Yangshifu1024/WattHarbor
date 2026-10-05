package ops

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
)

// 后台审计日志的查询，以及数据范围与定价目标核对所需的零散读取。
// 审计查询只读；写入由所属业务事务调用 ops.AppendAuditInTx。

// AuditQuery 是审计列表的筛选条件，零值表示该维度不限。
type AuditQuery struct {
	Module     string
	Action     string
	Actor      string
	TargetType string
	From       *time.Time // 起始时间（含），RFC 3339 解析后的 UTC
	To         *time.Time // 截止时间（含）
}

// AuditRow 映射 audit_log；可空字段用指针保留 NULL 语义——
// 审计里"没有目标"和"目标是空串"是两件事，不能互相顶替。
// 快照列在库中是 JSON，查询时 CAST 为 CHAR 后扫描成文本，
// 直接返回文本而不是让 encoding/json 把 []byte 编成 base64。
type AuditRow struct {
	ID         uint64    `json:"id"`          // 审计记录主键
	ActorID    uint64    `json:"actor_id"`    // 操作的后台账号 ID
	ActorName  string    `json:"actor_name"`  // 操作人登录名（当时的快照）
	Module     string    `json:"module"`      // 所属模块，等于目标类型，如 auth / coupon
	Action     string    `json:"action"`      // 动作，如 login / update / grant
	TargetType *string   `json:"target_type"` // 目标类型；指针，未指定对象时为 null
	TargetID   *string   `json:"target_id"`   // 目标主键（字符串形式）；指针，未指定时为 null
	RequestID  *string   `json:"request_id"`  // 关联请求号；指针，取不到时为 null
	ClientIP   *string   `json:"client_ip"`   // 客户端 IP；指针，取不到时为 null
	BeforeJSON *string   `json:"before_json"` // 变更前快照；指针，新建记录时为 null
	AfterJSON  *string   `json:"after_json"`  // 变更后快照；指针，新建记录时为 null
	CreatedAt  time.Time `json:"created_at"`  // 记录时间
}

// auditSelect 把两份快照 CAST 成文本，保证 int64 之类的列不会被当成
// JSON 扫描失败。
const auditSelect = "id, actor_id, actor_name, module, action, target_type, target_id, request_id, client_ip, " +
	"CAST(before_json AS CHAR) AS before_json, CAST(after_json AS CHAR) AS after_json, created_at"

// ListAudits 分页返回审计记录，按 ID 倒序。计数与列表复用同一套筛选条件。
func ListAudits(ctx context.Context, db *gorm.DB, q AuditQuery, page, pageSize int) (persist.Page[AuditRow], error) {
	out := persist.Page[AuditRow]{Items: []AuditRow{}, Page: page, PageSize: pageSize}
	filtered := func() *gorm.DB {
		query := db.WithContext(ctx).Table("audit_log")
		if q.Module != "" {
			query = query.Where("module = ?", q.Module)
		}
		if q.Action != "" {
			query = query.Where("action = ?", q.Action)
		}
		if q.Actor != "" {
			query = query.Where("actor_name LIKE ?", dbutil.LikePattern(q.Actor))
		}
		if q.TargetType != "" {
			query = query.Where("target_type = ?", q.TargetType)
		}
		if q.From != nil {
			query = query.Where("created_at >= ?", *q.From)
		}
		if q.To != nil {
			query = query.Where("created_at <= ?", *q.To)
		}
		return query
	}
	if err := filtered().Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := filtered().Select(auditSelect).Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out.Items).Error
	return out, err
}
