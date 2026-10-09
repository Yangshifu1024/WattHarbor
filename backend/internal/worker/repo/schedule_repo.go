package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 定时任务及执行日志的完整表操作，连接指向 worker_db；不查询 central 业务表。
//
// 调度用租约而不是长事务：领取时打上 lease_token 与到期时间，执行完再凭
// token 更新状态。token 未命中的更新影响 0 行，不更新已被其他执行者接管的任务。

// Task 是一条定时任务。数据库里的配置只控制调度参数，
// 真正的处理逻辑必须在代码里登记。
type Task struct {
	ID                   uint64     `gorm:"column:id"`                     // 定时任务主键。
	Code                 string     `gorm:"column:task_code"`              // 与代码中登记的处理器匹配的任务编码。
	CronExpr             string     `gorm:"column:cron_expr"`              // UTC 调度表达式，解析归调度用例。
	Enabled              bool       `gorm:"column:enabled"`                // 启用标志；强制手动执行可绕过。
	NextRunAt            *time.Time `gorm:"column:next_run_at"`            // 下次计划时间，NULL 不满足到期条件。
	LastRunAt            *time.Time `gorm:"column:last_run_at"`            // 最近完成时间，NULL 表示未记录。
	ConsecutiveFailCount uint32     `gorm:"column:consecutive_fail_count"` // 本次领取时的连续失败次数快照。
}

// ExecutionLog 是一次执行的结果。created_month 是分区列，与 id 共同定位一行。
type ExecutionLog struct {
	ID            uint64    `gorm:"column:id;primaryKey"`            // 创建后回填的日志主键，结束更新与月份共同定位。
	TaskCode      string    `gorm:"column:task_code"`                // 登记任务编码。
	StartedAt     time.Time `gorm:"column:started_at"`               // UTC 开始时间。
	CreatedMonth  time.Time `gorm:"column:created_month;primaryKey"` // 开始时间所在月首，作为分区复合主键。
	Status        string    `gorm:"column:status"`                   // 创建时 running，结束状态由用例提供。
	TriggeredBy   string    `gorm:"column:triggered_by"`             // cron 或 admin_api 触发来源。
	TriggerReason string    `gorm:"column:trigger_reason"`           // 手动触发原因，自动执行保留空串。
}

// TableName 返回执行日志表名，不执行 IO 或修改行。
func (ExecutionLog) TableName() string { return "task_execution_log" }

// CreateExecutionLog 向 db 写入 log 并由 GORM 回填 ID，返回写入错误。
// 保留 typed pointer、表名和复合主键映射，不更改用例构造值或自行提交外层事务。
func CreateExecutionLog(ctx context.Context, db *gorm.DB, log *ExecutionLog) error {
	return db.WithContext(ctx).Create(log).Error
}

// dueBatch 一次领取多少条到期任务。
const dueBatch = 10

// ListDueTasks 读取 db 中 codes 对应的到期启用任务，按计划时间和主键最多返回 10 条。
// now 同时核对计划与租约时间；空 codes 返回空列表，查询错误原样返回，不领取租约。
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

// FindTaskByCode 按 code 从 db 读取任务，返回记录或缺行／读取错误；不领取租约。
func FindTaskByCode(ctx context.Context, db *gorm.DB, code string) (Task, error) {
	var task Task
	err := db.WithContext(ctx).Table("scheduled_task").Where("task_code = ?", code).Take(&task).Error
	return task, err
}

// ClaimTaskLease 为 db 中 task.ID 竞争租约，lease 为新令牌，now 确定两分钟期限。
// force 跳过 enabled 检查，fromCron 仍要求到期；返回影响行数和写入错误，零行表示未领取，不提交外层事务。
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

// ReleaseLease 在 db 的 context 下按 taskID 和 lease 清空租约，返回写入错误，不释放其他令牌。
// 零行沿用成功语义，不自行提交或等待正在执行的处理器。
func ReleaseLease(db *gorm.DB, taskID uint64, lease string) error {
	return db.Table("scheduled_task").Where("id = ? AND lease_token = ?", taskID, lease).
		Updates(map[string]any{"lease_token": nil, "lease_until": nil}).Error
}

// FailStaleRunningLogs 把 db 中 taskCode 的 running 且 started_at 早于 before 的日志置 failed。
// at 是结束时间，失败原因保持原文本；返回写入错误，不清理已结束记录或领取新租约。
func FailStaleRunningLogs(ctx context.Context, db *gorm.DB, taskCode string, before, at time.Time) error {
	return db.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ? AND status = 'running' AND started_at < ?", taskCode, before).
		Updates(map[string]any{"status": "failed", "finished_at": at, "error_msg": "worker stopped before completion"}).Error
}

// FinishExecutionLog 按 db 中 id 和 month 全部主键字段保存 status、affectedRows、finishedAt 及 message。
// message 保持 any，nil 写 SQL NULL；返回写入错误，不替调用方判断状态、提交或改变租约。
func FinishExecutionLog(ctx context.Context, db *gorm.DB, id uint64, month time.Time, status string, affectedRows uint64, finishedAt time.Time, message any) error {
	return db.WithContext(ctx).Table("task_execution_log").
		Where("id = ? AND created_month = ?", id, month).
		Updates(map[string]any{
			"finished_at": finishedAt, "status": status,
			"affected_rows": affectedRows, "error_msg": message,
		}).Error
}

// FinishTask 按 db 中 taskID 和 lease 保存 finishedAt、nextRunAt、consecutiveFailures 并释放租约。
// 连续失败达到 5 次才写 enabled=false，其余情况不改变启用标志；返回影响行数和存储错误。
// 零行表示令牌未命中，不自行提交、重试或更新被其他执行者接管的任务。
func FinishTask(ctx context.Context, db *gorm.DB, taskID uint64, lease string, finishedAt, nextRunAt time.Time, consecutiveFailures uint32) (int64, error) {
	values := map[string]any{"last_run_at": finishedAt, "next_run_at": nextRunAt, "consecutive_fail_count": consecutiveFailures,
		"lease_token": nil, "lease_until": nil}
	if consecutiveFailures >= 5 {
		values["enabled"] = false
	}
	res := db.WithContext(ctx).Table("scheduled_task").Where("id = ? AND lease_token = ?", taskID, lease).Updates(values)
	return res.RowsAffected, res.Error
}

// LastExecution 从 db 按 taskCode 读取开始时间最新的日志记录，返回记录或读取错误。
// 缺行返回 gorm.ErrRecordNotFound，调用方按无记录形状展示；不修改日志或租约。
func LastExecution(ctx context.Context, db *gorm.DB, taskCode string) (LastExecutionRow, error) {
	var row LastExecutionRow
	err := db.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ?", taskCode).Order("started_at DESC").Take(&row).Error
	return row, err
}

// LastExecutionRow 是一次执行的时间与结果，不含租约等内部字段。
type LastExecutionRow struct {
	StartedAt  time.Time  `gorm:"column:started_at"`  // 开始时间。
	FinishedAt *time.Time `gorm:"column:finished_at"` // 结束时间，running 可为 NULL。
	Status     string     `gorm:"column:status"`      // 执行状态。
	ErrorMsg   *string    `gorm:"column:error_msg"`   // 错误文本，成功可为 NULL。
}

// CleanupExecutionHistory 从 db 按数据库时间删除最多 1000 条历史执行日志。
// 成功零行记录保留一天，其余 success／failed／partial 结束记录保留 30 天；running 不删，返回存储错误。
func CleanupExecutionHistory(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec(`DELETE FROM task_execution_log
		WHERE (status='success' AND affected_rows=0 AND finished_at<DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 1 DAY))
		OR (status IN ('success','failed','partial') AND finished_at<DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 30 DAY))
		LIMIT 1000`).Error
}
