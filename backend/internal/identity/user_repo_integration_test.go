//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/testfixture"
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

// TestUserRepoLoginCaseSensitiveIdentity 验证大小写不同的微信身份分别开户，重放恢复各自用户。
func TestUserRepoLoginCaseSensitiveIdentity(t *testing.T) {
	openIDs := []string{"identity-Case-" + uuid.NewString()}
	openIDs = append(openIDs, strings.ToLower(openIDs[0]))
	loginDistinctIdentities(t, openIDs)
}

// TestUserRepoLoginTrailingSpaceIdentity 验证身份尾随空格仍是不同字节，不受字符排序规则的补空格比较影响。
func TestUserRepoLoginTrailingSpaceIdentity(t *testing.T) {
	openID := "identity-space-" + uuid.NewString()
	loginDistinctIdentities(t, []string{openID, openID + " "})
}

// loginDistinctIdentities 验证两原始身份分别开户，并逐字节保留 OpenID 和 UnionID。
func loginDistinctIdentities(t *testing.T, openIDs []string) {
	t.Helper()
	db := identityDB(t)
	var users []uint64
	defer cleanupLoginUsers(t, db, &users, openIDs)
	repo := UserRepo{DB: db}
	for _, openID := range openIDs {
		user, err := repo.Login(t.Context(), openID, "union-"+openID)
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, user.ID)
		if !user.IsNew || user.OpenID != openID {
			t.Fatalf("new identity %q: %+v", openID, user)
		}
	}
	if users[0] == users[1] {
		t.Fatalf("byte-distinct identities share user %d", users[0])
	}
	for i, openID := range openIDs {
		user, err := repo.Login(t.Context(), openID, "")
		if err != nil || user.ID != users[i] || user.IsNew {
			t.Fatalf("replay %q: user=%+v err=%v", openID, user, err)
		}
		var stored struct {
			OpenID  string `gorm:"column:openid"`
			UnionID string `gorm:"column:unionid"`
		}
		if err := db.Table("user").Where("id = ?", users[i]).Take(&stored).Error; err != nil || stored.OpenID != openID || stored.UnionID != "union-"+openID {
			t.Fatalf("stored identity %q: got=%+v err=%v", openID, stored, err)
		}
	}
}

// TestUserRepoLoginAfterSoftDelete 验证软删释放有效身份唯一键，重新登录开户且保留历史用户。
func TestUserRepoLoginAfterSoftDelete(t *testing.T) {
	db := identityDB(t)
	openID := "identity-recreate-" + uuid.NewString()
	var users []uint64
	defer cleanupLoginUsers(t, db, &users, []string{openID})
	repo := UserRepo{DB: db}
	original, err := repo.Login(t.Context(), openID, "original-union")
	if err != nil {
		t.Fatal(err)
	}
	users = append(users, original.ID)
	if err := db.Table("user").Where("id = ?", original.ID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	recreated, err := repo.Login(t.Context(), openID, "replacement-union")
	if err != nil {
		t.Fatal(err)
	}
	users = append(users, recreated.ID)
	if !recreated.IsNew || recreated.ID == original.ID {
		t.Fatalf("original=%+v recreated=%+v", original, recreated)
	}
	for _, userID := range users {
		var count int64
		if err := db.Table("wallet_account").Where("user_id = ?", userID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("wallet for %d: count=%d err=%v", userID, count, err)
		}
	}
	var count int64
	if err := db.Table("user").Where("id = ? AND deleted_at IS NOT NULL", original.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("historical user count=%d err=%v", count, err)
	}
	replay, err := repo.Login(t.Context(), openID, "")
	if err != nil || replay.ID != recreated.ID || replay.IsNew {
		t.Fatalf("recreated replay=%+v err=%v", replay, err)
	}
}

// cleanupLoginUsers 仅按本测试创建的用户、钱包及身份完整主键删除，并核对零残留。
func cleanupLoginUsers(t *testing.T, db *gorm.DB, users *[]uint64, openIDs []string) {
	t.Helper()
	if len(*users) > 0 {
		var wallets []uint64
		if err := db.Table("wallet_account").Where("user_id IN ?", *users).Pluck("id", &wallets).Error; err != nil {
			t.Error(err)
		} else if len(wallets) > 0 {
			if err := db.Table("wallet_account").Where("id IN ?", wallets).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var count int64
			if err := db.Table("wallet_account").Where("id IN ?", wallets).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("wallet cleanup count=%d err=%v", count, err)
			}
		}
		if err := db.Table("user").Where("id IN ?", *users).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var count int64
		if err := db.Table("user").Where("id IN ?", *users).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("user cleanup count=%d err=%v", count, err)
		}
	}
	for _, openID := range openIDs {
		if err := db.Table("user_login_identity").Where("openid = ?", []byte(openID)).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var count int64
		if err := db.Table("user_login_identity").Where("openid = ?", []byte(openID)).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("identity cleanup %q count=%d err=%v", openID, count, err)
		}
	}
}

// TestUserAccountLiveOpenIDUnique 验证直接写库同样受有效身份唯一键保护；软删历史不占用该键。
func TestUserAccountLiveOpenIDUnique(t *testing.T) {
	tx := identityDB(t).WithContext(t.Context()).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	openID := "identity-unique-" + uuid.NewString()
	insert := func(userID uint64) error {
		return tx.Table("user").Create(map[string]any{"id": userID, "openid": openID, "status": "active"}).Error
	}
	if err := insert(id); err != nil {
		t.Fatal(err)
	}
	var duplicate *mysql.MySQLError
	if err := insert(id + 1); !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		t.Fatalf("duplicate live identity err=%v", err)
	}
	if err := tx.Table("user").Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if err := insert(id + 1); err != nil {
		t.Fatalf("replace deleted identity: %v", err)
	}
	var count int64
	if err := tx.Table("user").Where("id IN ? AND deleted_at IS NULL", []uint64{id, id + 1}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("live user count=%d err=%v", count, err)
	}
}

// TestUserWalletAccountAmountConstraints 验证开户钱包的金额边界由数据库兜底，夹具全部回滚。
func TestUserWalletAccountAmountConstraints(t *testing.T) {
	db := identityDB(t)
	for _, tc := range []struct {
		name    string
		balance int64
		frozen  int64
		valid   bool
	}{
		{name: "zero", valid: true},
		{name: "fully_frozen", balance: 100, frozen: 100, valid: true},
		{name: "partly_frozen", balance: 100, frozen: 50, valid: true},
		{name: "negative_balance", balance: -1},
		{name: "negative_frozen", balance: 100, frozen: -1},
		{name: "frozen_exceeds_balance", balance: 100, frozen: 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := db.WithContext(t.Context()).Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			id := testfixture.NewID()
			if err := tx.Table("wallet_account").Create(map[string]any{"id": id, "user_id": id, "balance_cents": 100, "frozen_cents": 0}).Error; err != nil {
				t.Fatal(err)
			}
			err := tx.Table("wallet_account").Where("id = ?", id).Updates(map[string]any{"balance_cents": tc.balance, "frozen_cents": tc.frozen}).Error
			if tc.valid {
				if err != nil {
					t.Fatalf("valid amounts rejected: %v", err)
				}
			} else {
				var constraint *mysql.MySQLError
				if !errors.As(err, &constraint) || constraint.Number != 3819 {
					t.Fatalf("invalid amounts accepted or wrong error: %v", err)
				}
			}
			var stored struct {
				BalanceCents int64
				FrozenCents  int64
			}
			if err := tx.Table("wallet_account").Where("id = ?", id).Take(&stored).Error; err != nil {
				t.Fatal(err)
			}
			wantBalance, wantFrozen := tc.balance, tc.frozen
			if !tc.valid {
				wantBalance, wantFrozen = 100, 0
			}
			if stored.BalanceCents != wantBalance || stored.FrozenCents != wantFrozen {
				t.Fatalf("stored wallet=%+v want balance=%d frozen=%d", stored, wantBalance, wantFrozen)
			}
		})
	}
}

// TestUserRepoConcurrentLoginOpensOneWallet 两独立连接在标识锁处控制并发，验证一用户、一开户及唯一创建者。
// 普通路径与一次重试错误测试替身分别验证；SQL、事务仍用真库，unionID归实际创建者而非请求启动顺序。
func TestUserRepoConcurrentLoginOpensOneWallet(t *testing.T) {
	for _, forceRetry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry_%t", forceRetry), func(t *testing.T) { concurrentLoginWallet(t, forceRetry) })
	}
}

// concurrentLoginWallet 用两条真实连接控制并发开户，可在首次锁后注入 1213 错误。
// 核对重试后只有一用户、一钱包与一位创建者，并按自身记录清理。
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
				// 仅首次锁后注入可重试错误，真实事务必须回滚；后续仍由Login有界重试。
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
// 一次绑定成功、一次返回归属冲突；用户解绑使用 SQL NULL，提交造数按自身 ID 清理。
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
	type result struct {
		uid uint64 /* 请求用户 */
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

// TestProfileReportsAvailableFundsAndWalletStatus 用真实联表投影验证部分/全部冻结及钱包独立风控状态。
func TestProfileReportsAvailableFundsAndWalletStatus(t *testing.T) {
	db := identityDB(t)
	for _, tc := range []struct {
		name   string
		frozen int64
		status string
		want   int64
	}{
		{"active", 0, "active", 10000}, {"reserved", 3000, "active", 7000}, {"fully_reserved", 10000, "active", 0}, {"risk_frozen", 3000, "frozen", 7000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			user, err := (UserRepo{DB: tx}).Login(t.Context(), "profile-"+uuid.NewString(), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Table("wallet_account").Where("user_id=?", user.ID).Updates(map[string]any{"balance_cents": 10000, "frozen_cents": tc.frozen, "status": tc.status}).Error; err != nil {
				t.Fatal(err)
			}
			profile, err := (UserRepo{DB: tx}).Profile(t.Context(), user.ID)
			if err != nil || profile.Wallet.AvailableCents != tc.want || profile.Wallet.FrozenCents != tc.frozen || string(profile.Wallet.Status) != tc.status {
				t.Fatalf("wallet=%+v err=%v", profile.Wallet, err)
			}
		})
	}
}
