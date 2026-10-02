package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 定时调度与死信运维的持久化访问。这几张表只属于 worker_db——worker 不读
// central 的业务表，要那些数据得经 central 的内部端点。
//
// 调度用租约而不是长事务：领取时打上 lease_token 与到期时间，执行完再凭
// token 推进状态。token 对不上的更新影响 0 行，说明租约已被别人接管，
// 这次执行的结果作废。

// Task 是一条定时任务。数据库里的配置只控制调度参数，
// 真正的处理逻辑必须在代码里登记。
type Task struct {
	ID                   uint64     `gorm:"column:id"`
	Code                 string     `gorm:"column:task_code"`
	CronExpr             string     `gorm:"column:cron_expr"`
	Enabled              bool       `gorm:"column:enabled"`
	NextRunAt            *time.Time `gorm:"column:next_run_at"`
	LastRunAt            *time.Time `gorm:"column:last_run_at"`
	ConsecutiveFailCount uint32     `gorm:"column:consecutive_fail_count"`
}

// ExecutionLog 是一次执行的结果。created_month 是分区列，与 id 共同定位一行。
type ExecutionLog struct {
	ID            uint64    `gorm:"column:id;primaryKey"`
	TaskCode      string    `gorm:"column:task_code"`
	StartedAt     time.Time `gorm:"column:started_at"`
	CreatedMonth  time.Time `gorm:"column:created_month;primaryKey"`
	Status        string    `gorm:"column:status"`
	TriggeredBy   string    `gorm:"column:triggered_by"`
	TriggerReason string    `gorm:"column:trigger_reason"`
}

func (ExecutionLog) TableName() string { return "task_execution_log" }

// dueBatch 一次领取多少条到期任务。
const dueBatch = 10

// ListDueTasks 返回到期且启用的任务，按计划时间与主键排序。
// codes 为空时返回空列表：调用方没有任何登记处理器，就不该执行任何任务。
func ListDueTasks(ctx context.Context, db *gorm.DB, codes []string, now time.Time) ([]Task, error) {
	tasks := []Task{}
	if len(codes) == 0 {
		return tasks, nil
	}
	err := db.WithContext(ctx).Table("scheduled_task").
		Where("task_code IN ? AND enabled = 1 AND next_run_at <= ? AND (lease_until IS NULL OR lease_until < ?)", codes, now, now).
		Order("next_run_at, id").Limit(dueBatch).Find(&tasks).Error
	return tasks, err
}

// FindTaskByCode 按任务编码取一条任务。
func FindTaskByCode(ctx context.Context, db *gorm.DB, code string) (Task, error) {
	var task Task
	err := db.WithContext(ctx).Table("scheduled_task").Where("task_code = ?", code).Take(&task).Error
	return task, err
}

// ClaimTaskLease 领取任务租约，返回是否领到。
// force 为真时无视 enabled 与 next_run_at——手动触发就是这样。
func ClaimTaskLease(ctx context.Context, db *gorm.DB, task Task, force, fromCron bool, lease string, now time.Time) (int64, error) {
	claim := db.WithContext(ctx).Table("scheduled_task").
		Where("id = ? AND (lease_until IS NULL OR lease_until < ?)", task.ID, now)
	if !force {
		claim = claim.Where("enabled = 1")
	}
	if fromCron {
		claim = claim.Where("next_run_at <= ?", now)
	}
	res := claim.Updates(map[string]any{"lease_token": lease, "lease_until": now.Add(2 * time.Minute)})
	return res.RowsAffected, res.Error
}

// ReleaseLease 释放任务租约。凭 token 释放，避免把别人接管后的租约也清掉。
func ReleaseLease(db *gorm.DB, taskID uint64, lease string) error {
	return db.Table("scheduled_task").Where("id = ? AND lease_token = ?", taskID, lease).
		Updates(map[string]any{"lease_token": nil, "lease_until": nil}).Error
}

// FailStaleRunningLogs 把超时未结束的 running 日志标记为失败。
// 进程崩在执行中途时日志会永远停在 running，下一次执行先把它收尾。
func FailStaleRunningLogs(ctx context.Context, db *gorm.DB, taskCode string, before, at time.Time) error {
	return db.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ? AND status = 'running' AND started_at < ?", taskCode, before).
		Updates(map[string]any{"status": "failed", "finished_at": at, "error_msg": "worker stopped before completion"}).Error
}

// FinishExecutionLog 写回一次执行的终态。凭 id + 分区月份定位。
// message 是错误文本，nil 表示这次执行没出错。保持 any 是为了与原实现
// "没有错误就写 NULL" 的行为一致，不在这里替调用方决定 nil 还是空串。
func FinishExecutionLog(ctx context.Context, db *gorm.DB, id uint64, month time.Time, status string, affectedRows uint64, finishedAt time.Time, message any) error {
	return db.WithContext(ctx).Table("task_execution_log").
		Where("id = ? AND created_month = ?", id, month).
		Updates(map[string]any{
			"finished_at": finishedAt, "status": status,
			"affected_rows": affectedRows, "error_msg": message,
		}).Error
}

// FinishTask 推进任务的调度状态并释放租约，返回是否真的推进了。
// 返回 0 表示租约在执行期间被别人接管，结果不应写回。
func FinishTask(db *gorm.DB, taskID uint64, lease string, values map[string]any) (int64, error) {
	res := db.Table("scheduled_task").Where("id = ? AND lease_token = ?", taskID, lease).Updates(values)
	return res.RowsAffected, res.Error
}

// ListExecutionLogs 分页返回执行日志，按 id 倒序。
func ListExecutionLogs(ctx context.Context, db *gorm.DB, taskCode string, page, pageSize int) ([]ExecutionLog, int64, error) {
	query := db.WithContext(ctx).Table("task_execution_log")
	if taskCode != "" {
		query = query.Where("task_code = ?", taskCode)
	}
	total := int64(0)
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []ExecutionLog{}
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

// DLQCursor 是一条死信流的重放进度。滑动游标保证每一轮都能向前推进。
type DLQCursor struct {
	Stream        string  `json:"stream" gorm:"column:stream"`
	LastID        *string `json:"last_id" gorm:"column:last_id"`
	ScannedTotal  uint64  `json:"scanned_total" gorm:"column:scanned_total"`
	ReplayedTotal uint64  `json:"replayed_total" gorm:"column:replayed_total"`
}

// dlqListLimit 一次列出多少条死信，够运维排查又不至于把响应撑爆。
const dlqListLimit = 100

// ListDLQEntries 列出某条流的死信记录，可按状态过滤。
func ListDLQEntries(ctx context.Context, db *gorm.DB, stream, status string) ([]map[string]any, error) {
	query := db.WithContext(ctx).Table("dlq_log").Where("stream = ?", stream)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	rows := []map[string]any{}
	err := query.Order("id DESC").Limit(dlqListLimit).Find(&rows).Error
	return rows, err
}

// FindDLQCursor 读回某条流的重放游标。
func FindDLQCursor(ctx context.Context, db *gorm.DB, stream string) ([]DLQCursor, error) {
	rows := []DLQCursor{}
	err := db.WithContext(ctx).Table("dlq_replay_cursor").Where("stream = ?", stream).Find(&rows).Error
	return rows, err
}

// LastExecution 是某任务最近一次执行的可读投影。
// 没跑过时返回 gorm.ErrRecordNotFound，调用方按"尚无执行记录"展示而不是报错。
func LastExecution(ctx context.Context, db *gorm.DB, taskCode string) (LastExecutionRow, error) {
	var row LastExecutionRow
	err := db.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ?", taskCode).Order("started_at DESC").Take(&row).Error
	return row, err
}

// LastExecutionRow 是一次执行的时间与结果，不含租约等内部字段。
type LastExecutionRow struct {
	StartedAt  time.Time  `gorm:"column:started_at"`
	FinishedAt *time.Time `gorm:"column:finished_at"`
	Status     string     `gorm:"column:status"`
	ErrorMsg   *string    `gorm:"column:error_msg"`
}

// CleanupExecutionHistory 清理历史执行日志：空轮询（跑过但没有处理任何行）
// 保留一天，其余已结束的保留 30 天。仍在 running 的不删——那不是"历史"，
// 是进程崩在执行中途留下的痕迹，由 FailStaleRunningLogs 收尾。
// 分批删除，单次 1000 行，避免一条 DELETE 锁住整张表。
func CleanupExecutionHistory(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec(`DELETE FROM task_execution_log
		WHERE (status='success' AND affected_rows=0 AND finished_at<DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 1 DAY))
		OR (status IN ('success','failed','partial') AND finished_at<DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 30 DAY))
		LIMIT 1000`).Error
}
