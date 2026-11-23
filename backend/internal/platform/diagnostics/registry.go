// Package diagnostics 记录后台任务的近期运行情况，供运维诊断读取。
package diagnostics

import (
	"context"
	"sync"
	"time"
)

// TaskStatus 只公开任务运行元数据。
type TaskStatus struct {
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	LastSuccess *time.Time `json:"last_success"`
	DurationMS  int64      `json:"duration_ms"`
	State       string     `json:"state"`
}

// Registry 按任务名维护最后一次运行状态。
type Registry struct {
	mu    sync.Mutex
	tasks map[string]TaskStatus
}

// Run 记录同步任务开始和完成，错误保留给原调用者处理。
func (r *Registry) Run(ctx context.Context, name string, runner func(context.Context) error) error {
	if r == nil {
		return runner(ctx)
	}
	start := time.Now()
	r.mu.Lock()
	status := r.tasks[name]
	status.StartedAt, status.FinishedAt, status.State = start, nil, "running"
	if r.tasks == nil {
		r.tasks = make(map[string]TaskStatus)
	}
	r.tasks[name] = status
	r.mu.Unlock()

	err := runner(ctx)
	end := time.Now()
	status.DurationMS, status.FinishedAt = end.Sub(start).Milliseconds(), &end
	if err != nil {
		status.State = "failed"
	} else {
		status.State, status.LastSuccess = "ok", &end
	}
	r.mu.Lock()
	r.tasks[name] = status
	r.mu.Unlock()
	return err
}

// Snapshot 提供可并发读取的独立副本。
func (r *Registry) Snapshot() map[string]TaskStatus {
	status := make(map[string]TaskStatus)
	if r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		for name, task := range r.tasks {
			status[name] = task
		}
	}
	return status
}
