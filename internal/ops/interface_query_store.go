package ops

import (
	"time"

	"gorm.io/gorm"
)

// InsertAudit 写一条审计记录。created_month 是分区列，必须显式给：

// 但落哪张表、有哪些列、created_month 怎么算都由这里决定。
type AuditEntryInput struct {
	ActorID    uint64
	ActorName  string
	Module     string
	Action     string
	TargetType string
	TargetID   string
	BeforeJSON []byte
	AfterJSON  []byte
	ClientIP   string
	RequestID  string
}

// InsertAudit 写一条审计记录。created_month 是分区列，必须显式给：
// 这张表按月分区，省略它会写到错误的分区或直接报错。
func InsertAudit(tx *gorm.DB, in AuditEntryInput, now time.Time) error {
	return tx.Table("audit_log").Create(map[string]any{
		"actor_id": in.ActorID, "actor_name": in.ActorName, "module": in.Module,
		"action": in.Action, "target_type": in.TargetType, "target_id": in.TargetID,
		"before_json": string(in.BeforeJSON), "after_json": string(in.AfterJSON),
		"client_ip": in.ClientIP, "request_id": in.RequestID,
		"created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
	}).Error
}
