package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

// retryFixture 注入仅测试使用的两项重试能力，不替换生产定时器或依赖装配。
type retryFixture struct {
	Compensate func(context.Context) error // 补偿步骤，接收运行时传入的同一 context。
	Pending    func(context.Context) error // 用户 STOP 步骤，用于核对顺序和取消边界。
}

// RetryStopping 调用 s.Compensate，返回其结果；生命周期由运行时及同步测试 bubble 管理。
func (s retryFixture) RetryStopping(ctx context.Context) error { return s.Compensate(ctx) }

// RetryPending 调用 s.Pending，返回其结果，不另建 goroutine 或改变任务身份。
func (s retryFixture) RetryPending(ctx context.Context) error { return s.Pending(ctx) }

// TestRetryStopsKeepsFiveSecondOrderAndContinuesAfterFailures 使用标准同步测试时钟跑五秒 ticker。
// 首轮两项失败不阻断后项或次轮，次轮主动取消结束；不缩短生产间隔或轮换恢复能力实例。
func TestRetryStopsKeepsFiveSecondOrderAndContinuesAfterFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start, calls, rounds := time.Now(), []string{}, 0
		fixture := retryFixture{
			Compensate: func(received context.Context) error {
				if received != ctx {
					t.Fatal("compensation context replaced")
				}
				rounds++
				calls = append(calls, "compensation")
				if rounds == 1 {
					return errors.New("private fixture compensation failure")
				}
				return nil
			},
			Pending: func(received context.Context) error {
				if received != ctx {
					t.Fatal("pending context replaced")
				}
				calls = append(calls, "pending")
				if rounds == 1 {
					return errors.New("private fixture pending failure")
				}
				cancel()
				return nil
			},
		}
		if err := (Runtime{Compensation: fixture, UserStops: fixture}).RetryStops(ctx); err != nil || time.Since(start) != 10*time.Second || !reflect.DeepEqual(calls, []string{"compensation", "pending", "compensation", "pending"}) {
			t.Fatalf("elapsed=%v calls=%v err=%v", time.Since(start), calls, err)
		}
	})
}

// TestRetryStopsDoesNotEnterPendingAfterCancellation 验证补偿中取消时不进入同轮用户 STOP，五秒首轮保持。
func TestRetryStopsDoesNotEnterPendingAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start, compensation, pending := time.Now(), 0, 0
		fixture := retryFixture{
			Compensate: func(context.Context) error { compensation++; cancel(); return context.Canceled },
			Pending:    func(context.Context) error { pending++; return nil },
		}
		if err := (Runtime{Compensation: fixture, UserStops: fixture}).RetryStops(ctx); err != nil || compensation != 1 || pending != 0 || time.Since(start) != 5*time.Second {
			t.Fatalf("elapsed=%v compensation=%d pending=%d err=%v", time.Since(start), compensation, pending, err)
		}
	})
}

// TestRetryStopsWithCanceledContextRunsNoTask 核对进入前取消的 context 立即退出，两项实际入口计数均为零。
func TestRetryStopsWithCanceledContextRunsNoTask(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start, calls := time.Now(), 0
		fixture := retryFixture{
			Compensate: func(context.Context) error { calls++; return nil },
			Pending:    func(context.Context) error { calls++; return nil },
		}
		if err := (Runtime{Compensation: fixture, UserStops: fixture}).RetryStops(ctx); err != nil || calls != 0 || time.Since(start) != 0 {
			t.Fatalf("elapsed=%v calls=%d err=%v", time.Since(start), calls, err)
		}
	})
}

// TestRetryStopsWaitsForInFlightCompensation 验证取消后仍等待正在执行的补偿返回，不提前释放其上层资源。
// synctest.Wait 确认补偿已阻塞于受管理出口；全部测试 goroutine 由 bubble 等待退出。
func TestRetryStopsWaitsForInFlightCompensation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		pending := 0
		fixture := retryFixture{
			Compensate: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				<-release
				return ctx.Err()
			},
			Pending: func(context.Context) error { pending++; return nil },
		}
		go func() { done <- (Runtime{Compensation: fixture, UserStops: fixture}).RetryStops(ctx) }()
		<-started
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned before compensation exit: %v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil || pending != 0 {
			t.Fatalf("pending=%d err=%v", pending, err)
		}
	})
}
