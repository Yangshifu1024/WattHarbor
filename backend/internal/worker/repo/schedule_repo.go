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

// TaskExecutionStatus 是 task_execution_log.status 列的执行结果状态，
// 用于执行历史查询、失败重跑判定与历史清理。
// 取值与 migrations/worker_db/0001_init.sql 中该 ENUM 列一致。
//
// 运行状态说明（重要）：当前 worker 调度器的处理器登记表为空
// （cmd/worker/app.go 中 Handlers 为空 map），Webhook、通用 DLQ 与监管投递均未装配。
// 因此本枚举在 Go 侧的写入路径目前只经测试调用，线上并未实际产生这些记录；
// 不得据此宣称该状态机已在线上运行或已有调度任务在写入。
type TaskExecutionStatus string

const (
	// TaskExecutionRunning 表示已领取租约并开始执行，尚未结束。
	TaskExecutionRunning TaskExecutionStatus = "running"
	// TaskExecutionSuccess 表示执行成功。affected_rows=0 的成功零行记录保留一天，其余保留 30 天。
	TaskExecutionSuccess TaskExecutionStatus = "success"
	// TaskExecutionFailed 表示执行失败；worker 中途退出遗留的 running 日志也会被改为该值。
	TaskExecutionFailed TaskExecutionStatus = "failed"
	// TaskExecutionPartial 表示部分成功。
	// 当前 Go 代码没有写入该状态的路径，它只出现在历史清理 SQL 中；
	// 保留该取值以完整覆盖列约束，且清理判定不依赖 Go 侧写入。
	TaskExecutionPartial TaskExecutionStatus = "partial"
)

// Known 判断是否属于该列的数据库合法集合（四值）。
func (s TaskExecutionStatus) Known() bool {
	switch s {
	case TaskExecutionRunning, TaskExecutionSuccess, TaskExecutionFailed, TaskExecutionPartial:
		return true
	default:
		return false
	}
}

// TerminalTaskExecutionStatuses 是执行已结束、可进入历史清理的状态集合。
// running 不在其中：未结束的记录不删。
const TerminalTaskExecutionStatuses = "success failed partial"

// terminalTaskExecutionStatuses 是 TerminalTaskExecutionStatuses 对应的取值切片，供 SQL 绑定使用。
// 白名单串是空格分隔的展示口径，不能直接拼进 IN 括号——那会得到单元素 IN ('success failed partial') 而匹配不到任何行。
var terminalTaskExecutionStatuses = []TaskExecutionStatus{TaskExecutionSuccess, TaskExecutionFailed, TaskExecutionPartial}

// TaskTriggeredBy 是 task_execution_log.triggered_by 列的触发来源，区分定时与后台手动。
// 取值与 migrations/worker_db/0001_init.sql 中该 ENUM 列一致。
type TaskTriggeredBy string

const (
	// TaskTriggeredByCron 表示由 cron 计划触发。
	TaskTriggeredByCron TaskTriggeredBy = "cron"
	// TaskTriggeredByAdminAPI 表示由运营后台手动触发。
	TaskTriggeredByAdminAPI TaskTriggeredBy = "admin_api"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (t TaskTriggeredBy) Known() bool {
	switch t {
	case TaskTriggeredByCron, TaskTriggeredByAdminAPI:
		return true
	default:
		return false
	}
}

// ExecutionLog 是一次执行的结果。created_month 是分区列，与 id 共同定位一行。
type ExecutionLog struct {
	ID            uint64              `gorm:"column:id;primaryKey"`            // 创建后回填的日志主键，结束更新与月份共同定位。
	TaskCode      string              `gorm:"column:task_code"`                // 登记任务编码。
	StartedAt     time.Time           `gorm:"column:started_at"`               // UTC 开始时间。
	CreatedMonth  time.Time           `gorm:"column:created_month;primaryKey"` // 开始时间所在月首，作为分区复合主键。
	Status        TaskExecutionStatus `gorm:"column:status"`                   // 创建时 running，结束状态由用例提供；词表见 TaskExecutionStatus。
	TriggeredBy   TaskTriggeredBy     `gorm:"column:triggered_by"`             // cron 或 admin_api 触发来源；词表见 TaskTriggeredBy。
	TriggerReason string              `gorm:"column:trigger_reason"`           // 手动触发原因，自动执行保留空串。
}

// TableName 返回执行日志表名，不执行 IO 或修改行。
func (ExecutionLog) TableName() string { return "task_execution_log" }

// CreateExecutionLog 保存执行日志，并回填自增 ID；沿用调用方事务。
func CreateExecutionLog(ctx context.Context, db *gorm.DB, log *ExecutionLog) error {
	return db.WithContext(ctx).Create(log).Error
}

// dueBatch 一次领取多少条到期任务。
const dueBatch = 10

// ListDueTasks 返回已登记、启用、到期且租约已过期的任务，最多 10 条。
// 按下次运行时间和 ID 排序；仅查询，不领取租约。
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

// FindTaskByCode 按任务编码查询；不存在时返回 gorm.ErrRecordNotFound。
func FindTaskByCode(ctx context.Context, db *gorm.DB, code string) (Task, error) {
	var task Task
	err := db.WithContext(ctx).Table("scheduled_task").Where("task_code = ?", code).Take(&task).Error
	return task, err
}

// ClaimTaskLease 领取两分钟租约，返回是否更新成功。
// force 可跳过暂停检查，但不能绕过有效租约；自动触发还要求任务已到期。
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

// ReleaseLease 清空该任务的匹配租约；令牌不同或记录不存在时不更新。
func ReleaseLease(db *gorm.DB, taskID uint64, lease string) error {
	return db.Table("scheduled_task").Where("id = ? AND lease_token = ?", taskID, lease).
		Updates(map[string]any{"lease_token": nil, "lease_until": nil}).Error
}

// FailStaleRunningLogs 将开始时间早于 before 的 running 日志标记为失败。
func FailStaleRunningLogs(ctx context.Context, db *gorm.DB, taskCode string, before, at time.Time) error {
	return db.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ? AND status = ? AND started_at < ?", taskCode, TaskExecutionRunning, before).
		Updates(map[string]any{"status": TaskExecutionFailed, "finished_at": at, "error_msg": "worker stopped before completion"}).Error
}

// FinishExecutionLog 按 ID 和月份保存执行状态、行数、结束时间和错误。
// message=nil 时清除错误字段。
// 本层不做准入校验：partial 等取值即使当前无写入路径也须能落库。
func FinishExecutionLog(ctx context.Context, db *gorm.DB, id uint64, month time.Time, status TaskExecutionStatus, affectedRows uint64, finishedAt time.Time, message any) error {
	return db.WithContext(ctx).Table("task_execution_log").
		Where("id = ? AND created_month = ?", id, month).
		Updates(map[string]any{
			"finished_at": finishedAt, "status": status,
			"affected_rows": affectedRows, "error_msg": message,
		}).Error
}

// FinishTask 按匹配租约保存进度和下次运行时间，并清空租约。
// 连续失败至少 5 次时暂停任务；令牌不匹配时不更新，也不会恢复已暂停任务。
func FinishTask(ctx context.Context, db *gorm.DB, taskID uint64, lease string, finishedAt, nextRunAt time.Time, consecutiveFailures uint32) (int64, error) {
	values := map[string]any{"last_run_at": finishedAt, "next_run_at": nextRunAt, "consecutive_fail_count": consecutiveFailures,
		"lease_token": nil, "lease_until": nil}
	if consecutiveFailures >= 5 {
		values["enabled"] = false
	}
	res := db.WithContext(ctx).Table("scheduled_task").Where("id = ? AND lease_token = ?", taskID, lease).Updates(values)
	return res.RowsAffected, res.Error
}

// LastExecution 读取任务最新开始的执行记录；不存在时返回 gorm.ErrRecordNotFound。
func LastExecution(ctx context.Context, db *gorm.DB, taskCode string) (LastExecutionRow, error) {
	var row LastExecutionRow
	err := db.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ?", taskCode).Order("started_at DESC").Take(&row).Error
	return row, err
}

// LastExecutionRow 是一次执行的时间与结果，不含租约等内部字段。
type LastExecutionRow struct {
	StartedAt  time.Time           `gorm:"column:started_at"`  // 开始时间。
	FinishedAt *time.Time          `gorm:"column:finished_at"` // 结束时间，running 可为 NULL。
	Status     TaskExecutionStatus `gorm:"column:status"`      // 执行状态；词表见 TaskExecutionStatus。
	ErrorMsg   *string             `gorm:"column:error_msg"`   // 错误文本，成功可为 NULL。
}

// CleanupExecutionHistory 从 db 按数据库时间删除最多 1000 条历史执行日志。
// 成功零行记录保留一天，其余 success／failed／partial 结束记录保留 30 天；running 不删，返回存储错误。
// SQL 中的状态字面量对应 task_execution_log.status 词表，删除语句不因类型化拆分；
// partial 即使 Go 侧无写入也参与判定，避免漏删历史遗留记录。
// status 的取值绑定 terminalTaskExecutionStatuses，不把展示用白名单串拼进 SQL 语法。
func CleanupExecutionHistory(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec(`DELETE FROM task_execution_log
		WHERE (status='success' AND affected_rows=0 AND finished_at<DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 1 DAY))
		OR (status IN ? AND finished_at<DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 30 DAY))
		LIMIT 1000`, terminalTaskExecutionStatuses).Error
}
