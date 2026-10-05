//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// identityDB 为 t 打开独立 TEST_MYSQL_URL 连接池，缺配置或失败终止；测试结束关闭自身连接。
func identityDB(t *testing.T) *gorm.DB {
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

// TestUserRepoConcurrentLoginOpensOneWallet 两独立连接在原身份锁处控制并发，验证一用户、一开户及唯一创建者。
// 普通路径与一次重试错误fixture分别验证；SQL／事务仍用真库，原unionID归实际创建者而非请求启动顺序。
func TestUserRepoConcurrentLoginOpensOneWallet(t *testing.T) {
	for _, forceRetry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry_%t", forceRetry), func(t *testing.T) { concurrentLoginWallet(t, forceRetry) })
	}
}

// concurrentLoginWallet 在t管理的两连接中以forceRetry选择首次锁后1213故障fixture。
// 无返回值；核对完整重试结果、唯一创建者及首次unionID，清理自身提交记录并核对零残留。
func concurrentLoginWallet(t *testing.T, forceRetry bool) {
	a, b := identityDB(t), identityDB(t)
	openid := "identity-concurrent-" + uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cleanup := func() {
		var ids []uint64
		if err := a.Table("user").Where("openid=?", openid).Pluck("id", &ids).Error; err != nil {
			t.Error(err)
			return
		}
		if len(ids) > 0 {
			if err := a.Table("wallet_account").Where("user_id IN ?", ids).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var remaining int64
			if err := a.Table("wallet_account").Where("user_id IN ?", ids).Count(&remaining).Error; err != nil || remaining != 0 {
				t.Errorf("cleanup wallet count=%d err=%v", remaining, err)
			}
		}
		if err := a.Table("user").Where("openid=?", openid).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		if err := a.Table("user_login_identity").Where("openid=?", []byte(openid)).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		for _, table := range []string{"user", "user_login_identity"} {
			var count int64
			if err := a.Table(table).Where("openid=?", openid).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("cleanup %s count=%d err=%v", table, count, err)
			}
		}
	}
	defer cleanup()
	locked, release, secondQuery := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var firstGate, secondGate atomic.Bool
	const callback = "identity_login_lock_gate"
	if err := a.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "user_login_identity" && db.Error == nil && firstGate.CompareAndSwap(false, true) {
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
				db.AddError(ctx.Err())
			}
			if forceRetry {
				// 仅首次锁后注入可重试错误，真实事务必须回滚；后续仍由原Login有界重试。
				db.AddError(&mysql.MySQLError{Number: 1213, Message: "login retry fixture"})
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.Callback().Query().Remove(callback)
	if err := b.Callback().Query().Before("gorm:query").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "user_login_identity" && secondGate.CompareAndSwap(false, true) {
			close(secondQuery)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer b.Callback().Query().Remove(callback)
	secondInsert := make(chan struct{})
	if err := b.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "user_login_identity" {
			close(secondInsert)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer b.Callback().Create().Remove(callback)
	// result 传递同一 openid 并发 Login 返回的身份、首次标志与错误；用户及钱包是否只创建一次另查真实表。
	type result struct {
		user User  /* 实际身份及首次标志 */
		err  error /* 登录结果 */
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { u, err := (UserRepo{DB: a}).Login(ctx, openid, "original-union"); first <- result{u, err} }()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// 首次请求已提交 identity 行并持有其锁；第二请求首先遇到 INSERT IGNORE 的同键等待。
	go func() { u, err := (UserRepo{DB: b}).Login(ctx, openid, "later-union"); second <- result{u, err} }()
	select {
	case <-secondInsert:
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
	if x.err != nil || y.err != nil || x.user.ID == 0 || x.user.ID != y.user.ID || x.user.IsNew == y.user.IsNew {
		t.Fatalf("first=%+v err=%v second=%+v err=%v", x.user, x.err, y.user, y.err)
	}
	select {
	case <-secondQuery:
	default:
		t.Fatal("second connection did not query original identity")
	}
	var count int64
	if err := a.Table("user").Where("openid=?", openid).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("user count=%d err=%v", count, err)
	}
	if err := a.Table("wallet_account").Where("user_id=?", x.user.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("wallet count=%d err=%v", count, err)
	}
	var union string
	wantUnion := "original-union"
	if y.user.IsNew {
		wantUnion = "later-union"
	}
	if err := a.Table("user").Where("id=?", x.user.ID).Pluck("unionid", &union).Error; err != nil || union != wantUnion {
		t.Fatalf("first union changed %s %v", union, err)
	}
	// 故障仅施加于当前连接的新钱包写入，不伪造事务或表；开户失败不得留下用户。
	broken := "identity-rollback-" + uuid.NewString()
	defer func() {
		if err := a.Table("user_login_identity").Where("openid=?", []byte(broken)).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
	}()
	fault := errors.New("identity wallet opening failure")
	const faultName = "identity_wallet_opening_fault"
	if err := a.Callback().Create().Before("gorm:create").Register(faultName, func(db *gorm.DB) {
		if db.Statement.Table == "wallet_account" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := (UserRepo{DB: a}).Login(ctx, broken, "")
	a.Callback().Create().Remove(faultName)
	if !errors.Is(err, fault) {
		t.Fatalf("opening failure=%v", err)
	}
	if err := a.Table("user").Where("openid=?", broken).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed opening left user=%d err=%v", count, err)
	}
	if err := a.Table("user_login_identity").Where("openid=?", []byte(broken)).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("retry identity=%d err=%v", count, err)
	}
}

// TestPhoneServiceConcurrentUniqueOwnership 用两独立连接闸门固定两个预查均未占用，再由真实唯一键仲裁号码。
// 一次绑定成功、一次返回归属冲突；原用户解绑使用 SQL NULL，提交造数按自身 ID 清理。
func TestPhoneServiceConcurrentUniqueOwnership(t *testing.T) {
	a, b := identityDB(t), identityDB(t)
	id := testfixture.NewID()
	tag := uuid.NewString()
	for i := range 2 {
		if err := a.Table("user").Create(map[string]any{"id": id + uint64(i), "openid": fmt.Sprintf("phone-%s-%d", tag, i), "status": "active"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := a.Table("user").Where("id IN ?", []uint64{id, id + 1}).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var n int64
		if err := a.Table("user").Where("id IN ?", []uint64{id, id + 1}).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("phone cleanup=%d err=%v", n, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready, release := make(chan struct{}, 2), make(chan struct{})
	const callback = "identity_phone_unique_gate"
	for _, db := range []*gorm.DB{a, b} {
		if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
			if q.Statement.Table == "user" && q.Error == nil {
				if _, ok := q.Statement.Dest.(*int64); ok {
					ready <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						q.AddError(ctx.Err())
					}
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer db.Callback().Query().Remove(callback)
	}
	number := fmt.Sprintf("188%08d", id%100000000)
	// result 关联手机号绑定申请的原用户与 Bind 错误，用于识别唯一归属获胜者；不携带提交事务或渠道确认对象。
	type result struct {
		uid uint64 /* 请求原用户 */
		err error  /* 唯一归属结果 */
	}
	done := make(chan result, 2)
	for i, db := range []*gorm.DB{a, b} {
		go func() {
			uid := id + uint64(i)
			_, err := (PhoneService{Users: UserRepo{DB: db}, Source: DevelopmentPhoneSource{}}).Bind(ctx, uid, PhoneInput{Phone: number})
			done <- result{uid, err}
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	won, lost := 0, 0
	var owner uint64
	for range 2 {
		select {
		case v := <-done:
			if v.err == nil {
				won++
				owner = v.uid
			} else if errors.Is(v.err, ErrPhoneTaken) {
				lost++
			} else {
				t.Fatal(v.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("phone won=%d lost=%d", won, lost)
	}
	for _, db := range []*gorm.DB{a, b} {
		db.Callback().Query().Remove(callback)
	}
	if _, err := (PhoneService{Users: UserRepo{DB: a}, Source: DevelopmentPhoneSource{}}).Bind(ctx, owner, PhoneInput{Phone: number}); err != nil {
		t.Fatalf("same owner replay=%v", err)
	}
	if err := (PhoneService{Users: UserRepo{DB: a}}).Unbind(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := a.Table("user").Where("id=? AND phone IS NULL", owner).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("unbind NULL=%d %v", n, err)
	}
}
