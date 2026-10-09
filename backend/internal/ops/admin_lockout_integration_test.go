//go:build integration

package ops

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 真库测试验证两个管理员并发互删仍保留一人。
// 只锁各自目标账号不足以保护数量；先锁共享角色行，再检查其他有效管理员。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 按 TEST_MYSQL_URL 打开真库，初始化最多十秒；配置或连接失败终止测试。
// 管理员计数仅统计本测试的 99001／99002，角色行锁保持真实；所有事务退出后关闭连接。
func testConn(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	raw := os.Getenv(testMySQLURLEnv)
	if raw == "" {
		t.Fatalf("%s 未设置。真库集成测试不提供跳过路径：没有库就是没测，不是测过了。", testMySQLURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDB, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatalf("连接 %s 失败：%v", raw, err)
	}
	orm, err := dbconn.WrapGORM(sqlDB)
	if err != nil {
		t.Fatalf("包装 GORM 失败：%v", err)
	}
	// 管理员计数仅限制本测试的两个账号，保留开发库已有管理员；仍用真实共享角色锁和独立连接。
	orm = orm.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "admin_user AS u" {
			return q.Where("u.id IN ?", []uint64{99001, 99002})
		}
		return q
	}).Session(&gorm.Session{})
	return orm, func() { _ = sqlDB.Close() }
}

const adminRoleCode = "customer_admin"

// 复用迁移预置的 customer_admin 角色，避免违反有效编码唯一键。
var adminRoleID uint64

// seedTwoAdmins 创建固定 ID 的两名有效管理员并复用预置角色；调用方负责身份独占和清理。
func seedTwoAdmins(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DELETE FROM admin_user WHERE id IN (99001, 99002)").Error; err != nil {
		t.Fatalf("清理旧账号失败：%v", err)
	}
	if err := db.Table("role").Where("code = ? AND deleted_at IS NULL", adminRoleCode).
		Select("id").Row().Scan(&adminRoleID); err != nil {
		t.Fatalf("读客户管理员角色失败：%v", err)
	}
	for i, id := range []uint64{99001, 99002} {
		if err := db.Exec("INSERT INTO admin_user (id,username,password_hash,role_id,status,auth_version,created_at) VALUES (?,?,?,?,'active',1,?)",
			id, "adm"+itoa(uint64(i)), "x", adminRoleID, time.Now().UTC()).Error; err != nil {
			t.Fatalf("造账号 %d 失败：%v", id, err)
		}
	}
}

// activeAdminCount 统计测试范围内未删除、启用且属于有效客户管理员角色的账号。
func activeAdminCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Table("admin_user AS u").
		Joins("JOIN role AS r ON r.id = u.role_id").
		Where("u.deleted_at IS NULL AND u.status = 'active' AND r.code = ? AND r.deleted_at IS NULL", adminRoleCode).
		Count(&n).Error; err != nil {
		t.Fatalf("统计有效管理员失败：%v", err)
	}
	return n
}

// itoa 将无符号整数转换为十进制字符串。
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// deleteOne 复现锁角色→检查剩余管理员→软删；关闭角色锁用于对照验证。
func deleteOne(db *gorm.DB, store AccountRepo, id uint64, lockRole bool) error {
	return db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		if lockRole {
			if err := store.LockCustomerAdminRole(tx); err != nil {
				return err
			}
		}
		remaining, err := store.CountOtherActiveAdminsInTx(context.Background(), tx, id)
		if err != nil {
			return err
		}
		if remaining == 0 {
			return errWouldLockOut
		}
		if _, err := store.LockAdminUser(tx, id); err != nil {
			return err
		}
		return SoftDeleteAdminUser(tx, id, 99001, time.Now().UTC())
	})
}

var errWouldLockOut = errors.New("would lock out")

// roleLockGate 把事务一卡在拿到角色行锁之后，由主控决定何时放行。
type roleLockGate struct {
	// locked 在首次取得目标锁后关闭，用于通知测试主控。
	locked chan struct{}
	// release 关闭后放行回调，不提交事务或释放行锁。
	release chan struct{}
	// once 保证锁点只通知和暂停一次。
	once sync.Once
	// passOne 保证放行通道最多关闭一次。
	passOne sync.Once
}

// pass 幂等放行测试回调，不释放数据库行锁。
func (g *roleLockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// TestConcurrentAdminDeletionKeepsOneActive 验证两个有效管理员并发互删只有一笔成功，后到者看到最新删除并拒绝。
func TestConcurrentAdminDeletionKeepsOneActive(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	seedTwoAdmins(t, setup)
	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() {
		janitor.Exec("DELETE FROM admin_user WHERE id IN (99001, 99002)")
	})

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	store1 := AccountRepo{DB: p1}
	store2 := AccountRepo{DB: p2}
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); <-start; errs[0] = deleteOne(p1, store1, 99001, true) }()
	wg.Add(1)
	go func() { defer wg.Done(); <-start; errs[1] = deleteOne(p2, store2, 99002, true) }()
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errWouldLockOut):
			// 预期：后到的那笔被拒
		default:
			t.Errorf("第 %d 笔报出预期之外的错误：%v", i, err)
		}
	}
	if succeeded != 1 {
		t.Errorf("两笔互删有 %d 笔成功，期望恰好 1 笔", succeeded)
	}
	if n := activeAdminCount(t, setup); n != 1 {
		t.Errorf("系统里还剩 %d 个有效客户管理员，期望 1 个——为 0 则后台被永久锁死", n)
	}
}

// TestRoleLockBlocksConcurrentDeletion 在首个事务取得角色锁后暂停，确认独立连接的第二事务被该锁阻塞。
func TestRoleLockBlocksConcurrentDeletion(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { janitor.Exec("DELETE FROM admin_user WHERE id IN (99001, 99002)") })
	seedTwoAdmins(t, setup)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := &roleLockGate{locked: make(chan struct{}), release: make(chan struct{})}
	if err := p1.Callback().Query().After("gorm:query").Register("test:role_lock_gate", func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, "FOR UPDATE") && strings.Contains(sql, "FROM `role`") {
			gate.once.Do(func() {
				close(gate.locked)
				<-gate.release
			})
		}
	}); err != nil {
		t.Fatalf("注册加锁回调失败：%v", err)
	}
	defer gate.pass()

	store1 := AccountRepo{DB: p1}
	store2 := AccountRepo{DB: p2}

	tx1 := p1.Begin()
	if tx1.Error != nil {
		t.Fatalf("事务一开启失败：%v", tx1.Error)
	}
	defer tx1.Rollback()

	done1 := make(chan error, 1)
	go func() { done1 <- deleteOne(tx1, store1, 99001, true) }()

	select {
	case <-gate.locked:
	case <-time.After(15 * time.Second):
		t.Fatal("事务一迟迟没有拿到角色行锁")
	}

	// 此刻事务一持有 role 行锁。事务二必须被挡住。
	tx2 := p2.Begin()
	if tx2.Error != nil {
		t.Fatalf("事务二开启失败：%v", tx2.Error)
	}
	defer tx2.Rollback()
	done2 := make(chan error, 1)
	go func() { done2 <- deleteOne(tx2, store2, 99002, true) }()

	select {
	case err := <-done2:
		t.Fatalf("事务一仍持角色行锁时事务二就返回了（%v）——那把锁没有串行化互删", err)
	case <-time.After(2 * time.Second):
		// 预期：被挡住。
	}

	gate.pass()
	if err := <-done1; err != nil {
		t.Fatalf("事务一应成功删除一个管理员，实际 %v", err)
	}
	// deleteOne 在外层事务中只建立保存点；必须提交外层事务，删除才可见、角色锁才释放。
	if err := tx1.Commit().Error; err != nil {
		t.Fatalf("事务一提交失败：%v", err)
	}
	if err := <-done2; !errors.Is(err, errWouldLockOut) {
		t.Errorf("事务二应因「再删就没有管理员了」被拒，实际 %v", err)
	}
	if n := activeAdminCount(t, setup); n != 1 {
		t.Errorf("系统里还剩 %d 个有效客户管理员，期望 1 个", n)
	}
}
