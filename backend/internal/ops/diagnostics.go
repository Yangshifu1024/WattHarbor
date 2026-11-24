package ops

import (
	"context"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/platform/diagnostics"
)

// QueueDepth 返回积压数量和最早的一笔记录时间；缺失统计由 unavailable 标记。
type QueueDepth struct {
	Count  int64      `json:"count"`
	Oldest *time.Time `json:"oldest_at"`
}

// DiagnosticsReply 是单个进程的诊断快照。
type DiagnosticsReply struct {
	Tasks       map[string]diagnostics.TaskStatus `json:"scanners"`
	Queues      map[string]QueueDepth             `json:"queues"`
	Unavailable []string                          `json:"unavailable"`
}

type queueQuery struct{ name, table, timestamp, filter string }

// CentralDiagnostics 读取中央域的事件、计费和导出积压。
func CentralDiagnostics(ctx context.Context, db *gorm.DB) DiagnosticsReply {
	return diagnosticsSnapshot(ctx, db, nil, []queueQuery{
		{"domain_events_pending", "event_outbox", "created_at", "status = 'pending'"},
		{"domain_events_failed", "event_outbox", "created_at", "status = 'failed'"},
		{"billing_pending", "charge_billing_job", "next_attempt_at", "status = 'pending'"},
		{"charging_over_2h", "charge_order", "started_at", "deleted_at IS NULL AND status = 'charging' AND started_at < UTC_TIMESTAMP(3) - INTERVAL 2 HOUR"},
		{"exports_processing", "export_task", "created_at", "status = 'running'"},
	})
}

// GatewayDiagnostics 读取网关待确认的启停命令。
func GatewayDiagnostics(ctx context.Context, db *gorm.DB, registry *diagnostics.Registry) DiagnosticsReply {
	return diagnosticsSnapshot(ctx, db, registry, []queueQuery{
		{"start_commands_sent", "charge_command", "sent_at", "status = 'sent'"},
		{"stop_commands_unacked", "charge_stop_command", "sent_at", "status = 'sent'"},
	})
}

// WorkerDiagnostics 读取 worker 后台任务状态。
func WorkerDiagnostics(registry *diagnostics.Registry) DiagnosticsReply {
	return DiagnosticsReply{Tasks: registry.Snapshot(), Queues: map[string]QueueDepth{}, Unavailable: []string{}}
}

func diagnosticsSnapshot(ctx context.Context, db *gorm.DB, registry *diagnostics.Registry, sources []queueQuery) DiagnosticsReply {
	out := WorkerDiagnostics(registry)
	for _, spec := range sources {
		var depth QueueDepth
		if err := db.WithContext(ctx).Table(spec.table).
			Select("COUNT(*) AS count, MIN(" + spec.timestamp + ") AS oldest").
			Where(spec.filter).Scan(&depth).Error; err != nil {
			out.Unavailable = append(out.Unavailable, spec.name)
			continue
		}
		out.Queues[spec.name] = depth
	}
	return out
}
