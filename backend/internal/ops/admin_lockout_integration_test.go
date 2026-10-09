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

// 客户管理员互删的真库并发测试。
//
// 「删完之后至少还剩一个」是跨行约束：判断要看**别的**账号行。
// 事务内原有的 LockAdminUser 只锁住被删的那一行，
// 两个管理员并发互删时两把锁落在不同的行上，互不阻塞，
// 两笔各自数到「还剩 1 个」而双双通过——最后一个都不剩，后台被永久锁死。
//
// 这里验证「锁角色行」确实是承重的：拿掉它，第二个事务就不被挡住。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 接收测试句柄 t，从必需的 TEST_MYSQL_URL 以十秒初始化 context 打开真实 SQL 池并包装 GORM。
// 成功返回 ORM 与关闭函数，由调用方在事务及任务结束后调用；缺配置、连接或包装错误由 t.Fatal 终止，不跳过测试。
// 返回的 ORM 只在管理员计数投影上限定账号 99001／99002，真实共享角色行锁与独立连接保留。
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
	// 现有开发库可有 bootstrap 管理员；本测试只统计自己创建的两名账号。
	// 作用域仅约束管理员计数投影，仍使用真实共享角色行及独立连接验证串行锁。
	orm = orm.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "admin_user AS u" {
			return q.Where("u.id IN ?", []uint64{99001, 99002})
		}
		return q
	}).Session(&gorm.Session{})
	return orm, func() { _ = sqlDB.Close() }
}

const adminRoleCode = "customer_admin"

// 角色由 migrations 预置（role.uk_active_code 保证 code 唯一），
// 测试复用现有那一行，不自己造——造会撞唯一键。
var adminRoleID uint64

// seedTwoAdmins 接收测试句柄 t 与真库句柄 db，删除固定账号 99001／99002，再读取预置 customer_admin 角色并插入两个 active 账号。
// 无返回值，SQL 失败由 t.Fatal 终止，并更新共享 adminRoleID；不创建角色或包装事务，调用方负责独占固定身份及清理已提交数据。
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

// activeAdminCount 接收测试句柄 t 与真库句柄 db，关联有效角色统计未删除、active 的客户管理员，并返回数量。
// 查询错误由 t.Fatal 终止；db 上既有作用域会限制测试账号，函数不添加锁、不修改账号或事务。
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

// itoa 接收无符号整数 v，返回其十进制字符串；零返回 0，无错误或外部副作用。
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

// deleteOne 复现 deleteAdminUser 事务体的关键两步：锁角色行 → 在锁内判断 → 软删。
// lockRole=false 时跳过角色行锁，用来验证那道锁确实是承重的。
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
	// locked 首次取得目标行锁后关闭的通知通道，由数据库回调关闭，控制方只接收。
	locked chan struct{}
	// release 控制方关闭后允许锁点回调继续的闸门，不提交数据库事务或自行释放行锁。
	release chan struct{}
	// once 确保首次到达锁点的通知及挂起仅发生一次，后续查询不重复进入同一闸门。
	once sync.Once
	// passOne 确保 release 通道最多关闭一次，显式放行与退出清理可重复调用。
	passOne sync.Once
}

// pass 无入参或返回值，最多一次关闭 g.release，放行正在等待的测试回调；可重复调用，不提交或释放数据库行锁。
func (g *roleLockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// TestConcurrentAdminDeletionKeepsOneActive 钉住互删不会把系统锁死。
//
// 两个有效客户管理员并发互相删除。必须恰好一笔成功：先到的那笔数到
// 「还剩 1 个」可以删，后到的那笔在角色行的当前读里看得见前一笔的软删除，
// 数到 0 而拒绝。若角色行锁被拿掉，两笔都会通过，系统里一个管理员都不剩。
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

// TestRoleLockBlocksConcurrentDeletion 是确定性的机制测试。
//
// 上一版这里写的是「拿掉角色行锁、起两个 goroutine、看会不会双双通过」——
// 连跑三次都只成功一笔，因为两个 goroutine 常常根本没真正重叠。
// **那样的测试证不了任何东西**，它只是没撞上时序，不是问题不存在。
//
// 改成用 gorm 回调把事务一精确卡在「刚拿到角色行锁」的那一刻，
// 再让事务二从另一条独立连接冲上来，断言它必须被数据库挡住。
// 拿掉那把锁，这个断言立刻失败。
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
	// 必须显式提交：deleteOne 传进来的是已开启的事务，gorm 只建了保存点，
	// 不提交外层事务的话，删除对事务二不可见，角色行锁也一直被占着。
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
