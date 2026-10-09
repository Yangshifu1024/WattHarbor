package ops

import (
	"chargepilot/internal/persist"
	"context"
	"time"

	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
)

// 本文件只读取审计日志；业务写入通过 ops.AppendAuditInTx 保存审计。

// AuditQuery 是审计列表的筛选条件，零值表示该维度不限。
type AuditQuery struct {
	Module     string     // 所属模块筛选
	Action     string     // 动作筛选
	Actor      string     // 登录名转义 LIKE 筛选
	TargetType string     // 目标类型筛选
	From       *time.Time // 起始时间（含），RFC 3339 解析后的 UTC
	To         *time.Time // 截止时间（含）
}

// AuditRow 是审计列表记录，可空字段保留 NULL。
// JSON 快照转成文本读取，避免 []byte 被响应编码为 base64。
type AuditRow struct {
	ID         uint64    `json:"id"`          // 审计记录主键
	ActorID    uint64    `json:"actor_id"`    // 操作的后台账号 ID
	ActorName  string    `json:"actor_name"`  // 操作人登录名（当时的快照）
	Module     string    `json:"module"`      // 所属模块，如 auth 或 coupon；认证审计的模块与目标类型不同
	Action     string    `json:"action"`      // 动作，如 login / update / grant
	TargetType *string   `json:"target_type"` // 目标类型；指针，未指定对象时为 null
	TargetID   *string   `json:"target_id"`   // 目标主键（字符串形式）；指针，未指定时为 null
	RequestID  *string   `json:"request_id"`  // 关联请求号；指针，取不到时为 null
	ClientIP   *string   `json:"client_ip"`   // 客户端 IP；指针，取不到时为 null
	BeforeJSON *string   `json:"before_json"` // 变更前 JSON 文本；SQL NULL 为 nil，JSON null 为文本 null
	AfterJSON  *string   `json:"after_json"`  // 变更后 JSON 文本；SQL NULL 为 nil，JSON null 为文本 null
	CreatedAt  time.Time `json:"created_at"`  // 记录时间
}

// auditSelect 将两份 JSON 快照转为文本，避免直接扫描 JSON 失败。
const auditSelect = "id, actor_id, actor_name, module, action, target_type, target_id, request_id, client_ip, " +
	"CAST(before_json AS CHAR) AS before_json, CAST(after_json AS CHAR) AS after_json, created_at"

// AuditRepo 只读取所属审计表，写入由各业务完整事务调用 AppendAuditInTx。
type AuditRepo struct {
	DB *gorm.DB /* 所属审计数据库 */
}

// List 用相同条件分别查询总数和列表，保留可空 JSON 文本；查询参数由 AuditQueryService 校验。
func (s AuditRepo) List(ctx context.Context, q AuditQuery, page, pageSize int) (persist.Page[AuditRow], error) {
	out := persist.Page[AuditRow]{Items: []AuditRow{}, Page: page, PageSize: pageSize}
	filtered := func() *gorm.DB {
		query := s.DB.WithContext(ctx).Table("audit_log")
		if q.Module != "" {
			query = query.Where("module = ?", q.Module)
		}
		if q.Action != "" {
			query = query.Where("action = ?", q.Action)
		}
		if q.Actor != "" {
			// 用 ! 转义关键词中的百分号和下划线，SQL 须显式声明转义字符，才能匹配含 _ 的账号名。
			query = query.Where("actor_name LIKE ? ESCAPE '!'", dbutil.LikePattern(q.Actor))
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
