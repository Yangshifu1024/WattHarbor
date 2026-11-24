//go:build integration

package order

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/testfixture"
)

// feedbackDB 为 t 打开独立 TEST_MYSQL_URL，缺配置或连接失败终止；测试退出关闭自身池。
func feedbackDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	pool, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// seedFeedbackOrder 为 t 用 db 创建自身已完成订单，返回订单与所有人身份；测试退出删除自身反馈和订单并核对零残留。
func seedFeedbackOrder(t *testing.T, db *gorm.DB) (uint64, uint64) {
	t.Helper()
	id := testfixture.NewID()
	userID := id + 1
	if err := db.Table("charge_order").Create(map[string]any{"id": id, "order_no": "FB" + uuid.NewString(), "user_id": userID, "device_id": "feedback-proof", "port_no": 1, "created_month": time.Now().UTC().Format("2006-01") + "-01", "status": "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, spec := range []struct {
			table, column string /* 本测试记录所在表和订单 ID 列 */
		}{{"feedback", "order_id"}, {"charge_order", "id"}} {
			if err := db.Table(spec.table).Where(spec.column+"=?", id).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := db.Table(spec.table).Where(spec.column+"=?", id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("cleanup %s=%d err=%v", spec.table, n, err)
			}
		}
	})
	return id, userID
}

// TestFeedbackCurrentReadRejectsDuplicateAfterOldRRSnapshot 固定旧 RR 快照早于另一连接的反馈提交，验证等锁后的当前读。
// 第二连接真实提交；第一事务回滚，最终清理订单与反馈，不依赖概率性并发。
func TestFeedbackCurrentReadRejectsDuplicateAfterOldRRSnapshot(t *testing.T) {
	a, b := feedbackDB(t), feedbackDB(t)
	orderID, userID := seedFeedbackOrder(t, a)
	tx := a.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var before int64
	if err := tx.Table("feedback").Where("order_id=?", orderID).Count(&before).Error; err != nil || before != 0 {
		t.Fatalf("old snapshot=%d %v", before, err)
	}
	in := FeedbackInput{Rating: 5, Category: "rating"}
	id, err := (FeedbackService{DB: b}).Submit(context.Background(), userID, orderID, in)
	if err != nil || id == 0 {
		t.Fatalf("committed feedback=%d err=%v", id, err)
	}
	if _, err := (FeedbackService{DB: tx}).Submit(context.Background(), userID, orderID, in); !errors.Is(err, ErrFeedbackExists) {
		t.Fatalf("old snapshot accepted duplicate err=%v", err)
	}
	var total int64
	if err := b.Table("feedback").Where("order_id=?", orderID).Count(&total).Error; err != nil || total != 1 {
		t.Fatalf("feedback total=%d err=%v", total, err)
	}
}

// TestFeedbackConcurrentSubmitSerializesOriginalOrder 以两连接与订单锁闸门固定提交顺序，核对一反馈和重复冲突。
// 同一完成订单先锁后查，提交造数只按自身身份清理；此测试不代表后台回复或设备报修验收。
func TestFeedbackConcurrentSubmitSerializesOriginalOrder(t *testing.T) {
	a, b := feedbackDB(t), feedbackDB(t)
	orderID, userID := seedFeedbackOrder(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, secondEntered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	const gate = "feedback_order_lock_gate"
	if err := a.Callback().Query().After("gorm:query").Register(gate, func(q *gorm.DB) {
		if q.Statement.Table == "charge_order" && q.Error == nil && once.CompareAndSwap(false, true) {
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
				q.AddError(ctx.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.Callback().Query().Remove(gate)
	if err := b.Callback().Query().Before("gorm:query").Register(gate, func(q *gorm.DB) {
		if q.Statement.Table == "charge_order" {
			close(secondEntered)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer b.Callback().Query().Remove(gate)
	type result struct {
		id  uint64 /* 实际反馈主键 */
		err error  /* 提交结果 */
	}
	first, second := make(chan result, 1), make(chan result, 1)
	in := FeedbackInput{Rating: 4, Category: "suggestion"}
	go func() { id, err := (FeedbackService{DB: a}).Submit(ctx, userID, orderID, in); first <- result{id, err} }()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		id, err := (FeedbackService{DB: b}).Submit(ctx, userID, orderID, in)
		second <- result{id, err}
	}()
	select {
	case <-secondEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(release)
	get := func(ch <-chan result) result {
		select {
		case v := <-ch:
			return v
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return result{}
		}
	}
	x, y := get(first), get(second)
	if x.id == 0 || x.err != nil || !errors.Is(y.err, ErrFeedbackExists) {
		t.Fatalf("first=%d %v second=%d %v", x.id, x.err, y.id, y.err)
	}
}

// TestFeedbackReplyAuditRollbackAndOriginalTransitions 验证真库回复、关闭、已关闭重放与审计故障整体回滚。
// 归属和状态检查拒绝他人、未完成订单；外层事务回滚所有造数和审计。
func TestFeedbackReplyAuditRollbackAndOriginalTransitions(t *testing.T) {
	db := feedbackDB(t)
	orderID, userID := seedFeedbackOrder(t, db)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	s := FeedbackService{DB: tx}
	ctx := context.Background()
	in := FeedbackInput{Rating: 5, Category: "complaint"}
	if _, err := s.Submit(ctx, userID+1, orderID, in); !errors.Is(err, ErrFeedbackOrderNotReady) {
		t.Fatalf("other owner=%v", err)
	}
	if err := tx.Table("charge_order").Where("id=?", orderID).Update("status", "charging").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, userID, orderID, in); !errors.Is(err, ErrFeedbackOrderNotReady) {
		t.Fatalf("unfinished=%v", err)
	}
	if err := tx.Table("charge_order").Where("id=?", orderID).Update("status", "refunded").Error; err != nil {
		t.Fatal(err)
	}
	id, err := s.Submit(ctx, userID, orderID, in)
	if err != nil {
		t.Fatal(err)
	}
	audit := ops.AuditContext{Actor: operations.Profile{ID: userID, Username: "feedback-proof", Permissions: []string{"feedback.reply"}}, RequestID: uuid.NewString()}
	fault := errors.New("feedback audit failure")
	const callback = "feedback_audit_fault"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = s.Reply(ctx, audit, id, FeedbackReplyInput{Action: "reply", ReplyContent: "verified response"})
	tx.Callback().Create().Remove(callback)
	if !errors.Is(err, fault) {
		t.Fatalf("audit failure=%v", err)
	}
	status, err := (FeedbackRepo{}).LockStatus(tx, id)
	if err != nil || status != "pending" {
		t.Fatalf("audit rollback status=%s err=%v", status, err)
	}
	if err := s.Reply(ctx, audit, id, FeedbackReplyInput{Action: "reply", ReplyContent: "verified response"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reply(ctx, audit, id, FeedbackReplyInput{Action: "reply", ReplyContent: "repeat"}); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("repeated reply=%v", err)
	}
	if err := s.Reply(ctx, audit, id, FeedbackReplyInput{Action: "close"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reply(ctx, audit, id, FeedbackReplyInput{Action: "close"}); err != nil {
		t.Fatal(err)
	}
	var audits int64
	if err := tx.Table("audit_log").Where("target_type='feedback' AND target_id=?", fmt.Sprint(id)).Count(&audits).Error; err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
}
