package ops

import (
	"time"

	"gorm.io/gorm"
)

// AuditEntryInput 是已编码的安全审计输入；表、列与分区月份由所属仓储决定。
type AuditEntryInput struct {
	ActorID    uint64 // 操作人身份
	ActorName  string // 操作时账号名
	Module     string // 所属业务模块
	Action     string // 操作名称
	TargetType string // 目标类型
	TargetID   string // 十进制或文本目标身份
	BeforeJSON []byte // 变更前安全快照的 JSON
	AfterJSON  []byte // 变更后安全快照的 JSON
	ClientIP   string // 首次请求客户端地址
	RequestID  string // 首次请求追踪标识
}

// InsertAudit 保存审计记录，必须显式填写月份分区键 created_month。
func InsertAudit(tx *gorm.DB, in AuditEntryInput, now time.Time) error {
	return tx.Table("audit_log").Create(map[string]any{
		"actor_id": in.ActorID, "actor_name": in.ActorName, "module": in.Module,
		"action": in.Action, "target_type": in.TargetType, "target_id": in.TargetID,
		"before_json": string(in.BeforeJSON), "after_json": string(in.AfterJSON),
		"client_ip": in.ClientIP, "request_id": in.RequestID,
		"created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
	}).Error
}
