// Package schedule 按数据库计划执行已登记处理器，管理租约、结果日志和失败暂停。
package schedule

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	workerrepo "wattharbor/internal/worker/repo"
)

// Handler 同步执行任务，须响应取消并在返回前结束自身工作。
type Handler func(context.Context) (uint64, error)

// Scheduler 领取租约并执行已登记任务，保存结果并安排下次运行。
type Scheduler struct {
	DB       *gorm.DB           // worker 定时任务及执行日志的受管理连接。
	Handlers map[string]Handler // 代码中登记的处理器，配置不能新增业务实现。
}

// Task 是 worker 仓储读取的定时任务配置。
type Task = workerrepo.Task

// CleanupHistory 调用历史清理，返回仓储错误；成功零行保留一天，其余结束记录保留 30 天。
// running 记录由 execute 恢复，不在本方法删除。
func (s Scheduler) CleanupHistory(ctx context.Context) error {
	return workerrepo.CleanupExecutionHistory(ctx, s.DB)
}

// parser 保留可选秒字段和描述式 UTC cron 语法，不接受每行时区覆盖。
var parser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// RunDue 读取已登记的到期任务并逐个执行，返回配置、读取或各任务执行错误的合并。
// 任务领取及租约在 execute 内处理，不因单任务失败跳过本轮其他已读取任务。
func (s Scheduler) RunDue(ctx context.Context) error {
	if s.DB == nil {
		return errors.New("scheduler database unavailable")
	}
	var due []Task
	now := time.Now().UTC()
	codes := make([]string, 0, len(s.Handlers))
	for code := range s.Handlers {
		codes = append(codes, code)
	}
	due, err := workerrepo.ListDueTasks(ctx, s.DB, codes, now)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	var failures []error
	for _, task := range due {
		if _, err := s.execute(ctx, task, false, workerrepo.TaskTriggeredByCron, ""); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", task.Code, err))
		}
	}
	return errors.Join(failures...)
}

// Trigger 按 code 手动执行登记任务，reason 为触发原因，force 允许执行暂停任务。
// 返回是否已领取并开始执行，以及未登记、配置、暂停或执行错误；竞争未领取返回 false/nil。
func (s Scheduler) Trigger(ctx context.Context, code, reason string, force bool) (bool, error) {
	if s.Handlers[code] == nil {
		return false, gorm.ErrRecordNotFound
	}
	if s.DB == nil {
		return false, errors.New("scheduler database unavailable")
	}
	task, err := workerrepo.FindTaskByCode(ctx, s.DB, code)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, gorm.ErrRecordNotFound
	}
	if err != nil {
		return false, err
	}
	if !task.Enabled && !force {
		return false, ErrPaused
	}
	return s.execute(ctx, task, force, workerrepo.TaskTriggeredByAdminAPI, reason)
}

// ErrPaused 表示任务已暂停，须强制手动触发才能执行。
var ErrPaused = errors.New("scheduled task paused")

// execute 领取两分钟租约，保存运行日志并使用 30 秒 context 执行处理器。
// 执行结束后用独立的 5 秒 context 保存结果、更新计划并清空租约；竞争失败返回 false。
func (s Scheduler) execute(ctx context.Context, task Task, force bool, triggeredBy workerrepo.TaskTriggeredBy, reason string) (bool, error) {
	handler := s.Handlers[task.Code]
	if handler == nil {
		return false, fmt.Errorf("handler unavailable for %s", task.Code)
	}
	spec, err := parseSpec(task.CronExpr)
	if err != nil {
		return false, fmt.Errorf("invalid cron for %s: %w", task.Code, err)
	}
	now := time.Now().UTC()
	lease := uuid.NewString()
	claimed, err := workerrepo.ClaimTaskLease(ctx, s.DB, task, force, triggeredBy == workerrepo.TaskTriggeredByCron, lease, now)
	if err != nil || claimed == 0 {
		return false, err
	}
	// 新尝试开始前，将过期租约遗留的 running 日志标记为结束。
	if err := workerrepo.FailStaleRunningLogs(ctx, s.DB, task.Code, now.Add(-2*time.Minute), now); err != nil {
		_ = workerrepo.ReleaseLease(s.DB.WithContext(context.Background()), task.ID, lease)
		return true, err
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	log := workerrepo.ExecutionLog{TaskCode: task.Code, StartedAt: now, CreatedMonth: month,
		Status: workerrepo.TaskExecutionRunning, TriggeredBy: triggeredBy, TriggerReason: reason}
	if err := workerrepo.CreateExecutionLog(ctx, s.DB, &log); err != nil {
		_ = workerrepo.ReleaseLease(s.DB.WithContext(context.Background()), task.ID, lease)
		return true, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rows, runErr := runSafely(runCtx, handler)
	cancel()
	finished := time.Now().UTC()
	status := workerrepo.TaskExecutionSuccess
	message := any(nil)
	failures := uint32(0)
	if runErr != nil {
		status = workerrepo.TaskExecutionFailed
		text := runErr.Error()
		if len(text) > 512 {
			text = text[:512]
		}
		message = text
		failures = task.ConsecutiveFailCount + 1
	}
	// 即使任务被取消，也用独立的 5 秒 context 保存结果并释放匹配租约。
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	logErr := workerrepo.FinishExecutionLog(finishCtx, s.DB, log.ID, month, status, rows, finished, message)
	advanced, taskErr := workerrepo.FinishTask(finishCtx, s.DB, task.ID, lease, finished, spec.Next(finished), failures)
	if taskErr == nil && advanced == 0 {
		taskErr = errors.New("scheduled task lease lost before completion")
	}
	return true, errors.Join(runErr, logErr, taskErr)
}

// parseSpec 解析最长 64 字节的 cron 表达式，拒绝 TZ= 时区覆盖；解析 panic 转为错误。
func parseSpec(raw string) (spec cron.Schedule, err error) {
	// 所有 worker 计划保留 UTC，拒绝逐行时区覆盖，不改变解析时基。
	if strings.Contains(raw, "TZ=") || len(raw) > 64 {
		return nil, errors.New("timezone override or oversized cron expression")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("cron parser panic: %v", recovered)
		}
	}()
	return parser.Parse(raw)
}

// runSafely 同步执行处理器，将 panic 转为错误供保存日志。
func runSafely(ctx context.Context, handler Handler) (rows uint64, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("scheduled task panic: %v", recovered)
		}
	}()
	return handler(ctx)
}
