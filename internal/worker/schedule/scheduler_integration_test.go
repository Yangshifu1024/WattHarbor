//go:build integration

package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// scheduleFixture 打开独立worker真库并提交仅本测试的任务；退出按日志完整分区主键及任务主键清理、核对零残留。
// 调用方须先取消并等待所有触发退出，不能把租约清理当作任务已经停止的证据。
func scheduleFixture(t *testing.T, failures uint32) (*gorm.DB, string, uint64) {
	t.Helper()
	url := os.Getenv("DATABASE_URL_WORKER")
	if url == "" {
		t.Fatal("DATABASE_URL_WORKER required")
	}
	pool, err := dbconn.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	code := "schedule_" + uuid.NewString()
	id := uint64(time.Now().UnixNano())
	t.Cleanup(func() {
		var rows []struct {
			ID    uint64    // 本测试执行日志主键。
			Month time.Time `gorm:"column:created_month"` // 原分区月份。
		}
		if err := db.Table("task_execution_log").Select("id,created_month").Where("task_code=?", code).Order("id,created_month").Find(&rows).Error; err != nil {
			t.Error(err)
			return
		}
		for _, row := range rows {
			if err := db.Table("task_execution_log").Where("id=? AND created_month=? AND task_code=?", row.ID, row.Month, code).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Table("scheduled_task").Where("id=? AND task_code=?", id, code).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		for _, table := range []string{"scheduled_task", "task_execution_log"} {
			var count int64
			if err := db.Table(table).Where("task_code=?", code).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("own fixture %s remaining=%d: %v", table, count, err)
			}
		}
	})
	if err := db.Table("scheduled_task").Create(map[string]any{"id": id, "task_code": code, "name": code, "cron_expr": "@every 1h", "enabled": true, "next_run_at": time.Now().UTC().Add(-time.Hour), "consecutive_fail_count": failures}).Error; err != nil {
		t.Fatal(err)
	}
	return db, code, id
}

// scheduleState 保留租约和原任务进度的真实可空数据库列，仅供验收读取。
type scheduleState struct {
	Lease    *string    `gorm:"column:lease_token"`            // 当前执行者租约，完成后应为空或保留接管者。
	Until    *time.Time `gorm:"column:lease_until"`            // 当前租约到期时间。
	Last     *time.Time `gorm:"column:last_run_at"`            // 原完成时间；未完成不可提前推进。
	Next     *time.Time `gorm:"column:next_run_at"`            // 原UTC下次执行时间。
	Failures uint32     `gorm:"column:consecutive_fail_count"` // 原连续失败次数。
	Enabled  bool       `gorm:"column:enabled"`                // 达五次失败后禁用，force不隐式启用。
}

// readScheduleState 读取本任务主键对应的真实状态，存储错误终止当前验收。
func readScheduleState(t *testing.T, db *gorm.DB, id uint64) scheduleState {
	t.Helper()
	var row scheduleState
	if err := db.Table("scheduled_task").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// TestRegisteredSchedulerPersistsOriginalExecution 以t验证实际注册trigger、日志主键回填、原完成列和UTC下一轮。
// handler为固定计数窄fixture，不执行业务表、设备或支付操作；真实SQL只写本测试worker任务与日志。
func TestRegisteredSchedulerPersistsOriginalExecution(t *testing.T) {
	db, code, id := scheduleFixture(t, 3)
	var calls atomic.Int64
	s := Scheduler{DB: db, Handlers: map[string]Handler{code: func(ctx context.Context) (uint64, error) {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			return 0, errors.New("original managed handler deadline missing")
		}
		return 4, nil
	}}}
	router := httpapi.NewRouter()
	API{Scheduler: s, ServiceToken: "registered-schedule"}.Register(router)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/scheduled-tasks/"+code+"/trigger", strings.NewReader(`{"trigger_reason":"registered fixture","force":false}`))
	r.Header.Set("X-Service-Token", "registered-schedule")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	var reply struct {
		Code int `json:"code"` // 原业务成功码。
		Data struct {
			Status string `json:"status"` // 原任务同步完成状态。
		} `json:"data"` // 原公开数据载荷。
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 200 || reply.Code != 0 || reply.Data.Status != "success" || calls.Load() != 1 {
		t.Fatalf("registered result=%d %s calls=%d: %v", w.Code, w.Body.String(), calls.Load(), err)
	}
	state := readScheduleState(t, db, id)
	if state.Lease != nil || state.Until != nil || state.Last == nil || state.Next == nil || !state.Next.After(*state.Last) || state.Failures != 0 || !state.Enabled {
		t.Fatalf("original successful task state=%+v", state)
	}
	var log struct {
		ID       uint64     // 实际执行日志主键，须已回填并更新同一行。
		Status   string     // 原执行终态。
		Rows     uint64     `gorm:"column:affected_rows"`  // 原handler计数。
		Error    *string    `gorm:"column:error_msg"`      // 成功必须保留SQL NULL。
		Finished *time.Time `gorm:"column:finished_at"`    // 原执行完成时间。
		Reason   string     `gorm:"column:trigger_reason"` // 原手动触发原因。
		Source   string     `gorm:"column:triggered_by"`   // 原admin_api来源。
	}
	if err := db.Table("task_execution_log").Where("task_code=?", code).Take(&log).Error; err != nil || log.ID == 0 || log.Status != "success" || log.Rows != 4 || log.Error != nil || log.Finished == nil || log.Reason != "registered fixture" || log.Source != "admin_api" {
		t.Fatalf("original execution log=%+v: %v", log, err)
	}
}

// TestSchedulerLeaseExclusionCancellationAndTakeover 用t固定handler进入后才进行另一真实触发及取消。
// 租约未到期时force也不能重复运行；取消后原独立5秒落盘继续，原token失效不得覆盖接管者状态。
func TestSchedulerLeaseExclusionCancellationAndTakeover(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled completion", true: "new lease protected"}[takeover], func(t *testing.T) {
			db, code, id := scheduleFixture(t, 0)
			started := make(chan struct{})
			var calls atomic.Int64
			s := Scheduler{DB: db, Handlers: map[string]Handler{code: func(ctx context.Context) (uint64, error) {
				calls.Add(1)
				close(started)
				<-ctx.Done()
				return 2, ctx.Err()
			}}}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			type outcome struct {
				accepted bool  // 原触发是否已经取得租约。
				err      error // 原执行及落盘聚合错误。
			}
			result := make(chan outcome, 1)
			go func() { ok, err := s.Trigger(ctx, code, "cancellation", false); result <- outcome{ok, err} }()
			received := false
			defer func() {
				cancel()
				if !received {
					select {
					case <-result:
					case <-time.After(7 * time.Second):
						t.Error("trigger did not exit before fixture cleanup")
					}
				}
			}()
			select {
			case <-started:
			case out := <-result:
				received = true
				t.Fatalf("trigger exited before handler: %+v", out)
			case <-ctx.Done():
				t.Fatal("handler did not start")
			}
			other := Scheduler{DB: db, Handlers: map[string]Handler{code: func(context.Context) (uint64, error) {
				calls.Add(1)
				return 9, nil
			}}}
			if ok, err := other.Trigger(t.Context(), code, "overlapping force", true); ok || err != nil || calls.Load() != 1 {
				t.Fatalf("active lease allowed duplicate accepted=%t calls=%d: %v", ok, calls.Load(), err)
			}
			newLease := uuid.NewString()
			if takeover {
				// fixture模拟原租约过期后的另一个执行者，不新建任务或触发第二次handler。
				if err := db.Table("scheduled_task").Where("id=?", id).Updates(map[string]any{"lease_token": newLease, "lease_until": time.Now().UTC().Add(time.Minute)}).Error; err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case out := <-result:
				received = true
				if !out.accepted || !errors.Is(out.err, context.Canceled) || takeover && !strings.Contains(out.err.Error(), "lease lost") {
					t.Fatalf("original canceled trigger=%+v", out)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("canceled handler did not finish managed persistence")
			}
			state := readScheduleState(t, db, id)
			if takeover {
				if state.Lease == nil || *state.Lease != newLease || state.Until == nil || state.Last != nil || state.Failures != 0 {
					t.Fatalf("old attempt overwrote new lease=%+v", state)
				}
			} else if state.Lease != nil || state.Until != nil || state.Last == nil || state.Failures != 1 {
				t.Fatalf("cancellation prevented original completion=%+v", state)
			}
			var count int64
			if err := db.Table("task_execution_log").Where("task_code=? AND status='failed' AND affected_rows=2 AND finished_at IS NOT NULL", code).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("original canceled log count=%d: %v", count, err)
			}
		})
	}
}

// TestSchedulerFailureThresholdAndForcePreserveDisabledState 验证原第五次失败禁用及force仅允许执行、不隐式重新启用。
func TestSchedulerFailureThresholdAndForcePreserveDisabledState(t *testing.T) {
	db, code, id := scheduleFixture(t, 4)
	var calls atomic.Int64
	want := errors.New("fixed schedule failure")
	s := Scheduler{DB: db, Handlers: map[string]Handler{code: func(context.Context) (uint64, error) { calls.Add(1); return 0, want }}}
	if ok, err := s.Trigger(t.Context(), code, "fifth failure", false); !ok || !errors.Is(err, want) {
		t.Fatalf("fifth failure accepted=%t: %v", ok, err)
	}
	if state := readScheduleState(t, db, id); state.Enabled || state.Failures != 5 || state.Lease != nil {
		t.Fatalf("original fifth failure state=%+v", state)
	}
	if ok, err := s.Trigger(t.Context(), code, "paused", false); ok || !errors.Is(err, ErrPaused) || calls.Load() != 1 {
		t.Fatalf("paused task accepted=%t calls=%d: %v", ok, calls.Load(), err)
	}
	if ok, err := s.Trigger(t.Context(), code, "explicit force", true); !ok || !errors.Is(err, want) || calls.Load() != 2 {
		t.Fatalf("force accepted=%t calls=%d: %v", ok, calls.Load(), err)
	}
	if state := readScheduleState(t, db, id); state.Enabled || state.Failures != 6 || state.Lease != nil {
		t.Fatalf("force changed original disabled state=%+v", state)
	}
}
