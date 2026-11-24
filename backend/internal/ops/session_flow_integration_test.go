//go:build integration

package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/dbconn"
)

// securityFixture 用真库和 Redis 创建本测试的角色、账号和会话；不修改初始管理员。
// 结束后只清理自己的记录及认证缓存，并检查零残留。
func securityFixture(t *testing.T) (*gorm.DB, *gorm.DB, SessionRepo, AdminAccount, string, *auth.JWT) {
	t.Helper()
	ctx := context.Background()
	open := func() *gorm.DB {
		raw := os.Getenv("TEST_MYSQL_URL")
		if raw == "" {
			t.Fatal("TEST_MYSQL_URL is required")
		}
		pool, err := dbconn.Open(ctx, raw)
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
	first, second := open(), open()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(options)
	t.Cleanup(func() { cache.Close() })
	tag := uuid.NewString()
	password := "Test!SecurityPassword2026"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	role := RoleRow{Code: "security-" + tag, Name: "security fixture", IsBuiltin: false}
	if err := first.Table("role").Select("code", "name", "is_builtin").Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"role_permission", "role"} {
			column := "id"
			if table == "role_permission" {
				column = "role_id"
			}
			if err := first.Table(table).Where(column+"=?", role.ID).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var count int64
			if err := first.Table(table).Where(column+"=?", role.ID).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("cleanup %s count=%d err=%v", table, count, err)
			}
		}
	})
	account := AdminAccount{Username: "security-" + tag, PasswordHash: string(hash), RoleID: role.ID, Status: "active", AuthVersion: 9007199254741111}
	if err := first.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"audit_log", "admin_user"} {
			column := "id"
			switch table {
			case "audit_log":
				column = "actor_id"
			}
			if err := first.Table(table).Where(column+"=?", account.ID).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var count int64
			if err := first.Table(table).Where(column+"=?", account.ID).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("cleanup %s count=%d err=%v", table, count, err)
			}
		}
		if err := cache.Del(ctx, mfaPendingKey(account.ID), mfaLockKey(account.ID), mfaSettingsRateKey(account.ID)).Err(); err != nil {
			t.Error(err)
		}
	})
	jwt, err := auth.NewJWT(strings.Repeat("fixture-security-", 3))
	if err != nil {
		t.Fatal(err)
	}
	return first, second, SessionRepo{Redis: cache}, account, password, jwt
}

// TestSecuritySessionLiveVersionAndMFAAdmission 用真实 SQL 和 Redis 验证撤权、改密、挑战版本和锁定检查。
// 坏码、锁定或旧版本不删除挑战；成功删除后重放拒绝，仅清理本测试 SID。
func TestSecuritySessionLiveVersionAndMFAAdmission(t *testing.T) {
	db, _, repo, account, password, jwt := securityFixture(t)
	ctx := context.Background()
	service := SessionService{Accounts: AccountRepo{DB: db}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
	var permissionID uint64
	if err := db.Table("permission").Where("code='order.read'").Pluck("id", &permissionID).Error; err != nil || permissionID == 0 {
		t.Fatalf("permission fixture %d %v", permissionID, err)
	}
	if err := db.Table("role_permission").Create(map[string]any{"role_id": account.RoleID, "permission_id": permissionID}).Error; err != nil {
		t.Fatal(err)
	}
	track := func(reply AccessReply) string {
		_, sid, err := (JWTAccess{JWT: jwt}).Verify(reply.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := repo.RevokeSession(ctx, sid); err != nil {
				t.Error(err)
			}
		})
		return sid
	}
	login, err := service.Login(ctx, account.Username, password, "fixture")
	if err != nil || login.Challenge != "" || login.Access.RefreshToken == "" {
		t.Fatalf("login %+v %v", login, err)
	}
	sid := track(login.Access)
	if p, _, err := service.Authenticate(ctx, login.Access.AccessToken, ""); err != nil || p.AuthVersion != account.AuthVersion {
		t.Fatalf("live profile %+v %v", p, err)
	}
	if _, _, err := service.Authenticate(ctx, login.Access.AccessToken, "order.read"); err != nil {
		t.Fatalf("current permission %v", err)
	}
	if err := db.Table("role_permission").Where("role_id=? AND permission_id=?", account.RoleID, permissionID).Delete(map[string]any{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, login.Access.AccessToken, "order.read"); !errors.Is(err, ErrPermission) {
		t.Fatalf("permission %v", err)
	}
	if err := db.Table("admin_user").Where("id=?", account.ID).Update("auth_version", gorm.Expr("auth_version+1")).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, login.Access.AccessToken, ""); !errors.Is(err, operations.ErrCredentials) {
		t.Fatalf("revocation %v", err)
	}
	if _, err := service.Refresh(ctx, login.Access.RefreshToken); !errors.Is(err, operations.ErrCredentials) {
		t.Fatalf("refresh version %v", err)
	}
	if exists, err := repo.Exists(ctx, sid); err != nil || exists {
		t.Fatalf("invalid refresh retained %v %v", exists, err)
	}
	login, err = service.Login(ctx, account.Username, password, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	track(login.Access)
	if err := service.ChangePassword(ctx, account.ID, password, "Next!SecurityPassword2026", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, login.Access.AccessToken, ""); !errors.Is(err, operations.ErrCredentials) {
		t.Fatalf("password revoke %v", err)
	}
	password = "Next!SecurityPassword2026"
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{"mfa_enabled": true, "mfa_secret": secret}).Error; err != nil {
		t.Fatal(err)
	}
	login, err = service.Login(ctx, account.Username, password, "fixture")
	if err != nil || login.Challenge == "" || login.Access.AccessToken != "" {
		t.Fatalf("challenge %+v %v", login, err)
	}
	t.Cleanup(func() { repo.Redis.Del(ctx, mfaKey(login.Challenge)) })
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bad := "abcdef"
	if _, err := service.VerifyMFA(ctx, login.Challenge, bad, "fixture"); !errors.Is(err, operations.ErrInvalidTOTP) {
		t.Fatalf("bad proof %v", err)
	}
	if _, err := repo.ResolveMFA(ctx, login.Challenge); err != nil {
		t.Fatalf("bad code consumed challenge %v", err)
	}
	if err := db.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{"status": "locked", "locked_until": time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	lockedReply, lockedErr := service.VerifyMFA(ctx, login.Challenge, code, "fixture")
	if lockedReply.AccessToken != "" {
		track(lockedReply)
	}
	if err := lockedErr; !errors.Is(err, operations.ErrLocked) {
		t.Fatalf("locked MFA accepted %v", err)
	}
	if err := db.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{"status": "active", "locked_until": nil, "auth_version": gorm.Expr("auth_version+1")}).Error; err != nil {
		t.Fatal(err)
	}
	staleReply, staleErr := service.VerifyMFA(ctx, login.Challenge, code, "fixture")
	if staleReply.AccessToken != "" {
		track(staleReply)
	}
	if err := staleErr; !errors.Is(err, operations.ErrInvalidMFAChallenge) {
		t.Fatalf("stale challenge accepted %v", err)
	}
	if _, err := repo.ResolveMFA(ctx, login.Challenge); err != nil {
		t.Fatalf("stale challenge consumption %v", err)
	}
	if err := repo.Redis.Del(ctx, mfaKey(login.Challenge)).Err(); err != nil {
		t.Fatal(err)
	}
	login, err = service.Login(ctx, account.Username, password, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	code, err = auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := service.VerifyMFA(ctx, login.Challenge, code, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	track(reply)
	if _, err := service.VerifyMFA(ctx, login.Challenge, code, "fixture"); !errors.Is(err, operations.ErrInvalidMFAChallenge) {
		t.Fatalf("challenge replay %v", err)
	}
}

// TestSecurityMFAChallengeVersionUsesLockedCurrentRead 验证另一连接提升版本后，旧 RR 快照下锁读仍拒绝旧挑战；只清理本测试数据。
func TestSecurityMFAChallengeVersionUsesLockedCurrentRead(t *testing.T) {
	first, second, repo, account, _, jwt := securityFixture(t)
	ctx := context.Background()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{"mfa_enabled": true, "mfa_secret": secret}).Error; err != nil {
		t.Fatal(err)
	}
	challenge, err := repo.BeginMFA(ctx, account.Identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Redis.Del(ctx, mfaKey(challenge)) })
	tx := first.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var before AdminAccount
	if err := tx.Where("id=?", account.ID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := second.Table("admin_user").Where("id=?", account.ID).Update("auth_version", gorm.Expr("auth_version+1")).Error; err != nil {
		t.Fatal(err)
	}
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	service := SessionService{Accounts: AccountRepo{DB: tx}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
	reply, err := service.VerifyMFA(ctx, challenge, code, "fixture")
	if reply.AccessToken != "" {
		_, sid, parseErr := (JWTAccess{JWT: jwt}).Verify(reply.AccessToken)
		if parseErr == nil {
			repo.RevokeSession(ctx, sid)
		}
	}
	if _, err := repo.ResolveMFA(ctx, challenge); err != nil {
		t.Fatalf("denied version consumed %v", err)
	}
	if !errors.Is(err, operations.ErrInvalidMFAChallenge) || reply.AccessToken != "" {
		t.Fatalf("old snapshot was not rejected before consumption version=%d err=%v", before.AuthVersion, err)
	}
}

// trackedSecuritySessions 使用真实存储并记录新 SID，供签发失败清理检查。
type trackedSecuritySessions struct {
	SessionRepo        // 创建／轮换／撤销行为
	createdSID  string // 创建返回的唯一 SID
}

// Create 创建真实会话并记住 SID。
func (s *trackedSecuritySessions) Create(ctx context.Context, account operations.AccountIdentity) (string, string, error) {
	sid, refresh, err := s.SessionRepo.Create(ctx, account)
	s.createdSID = sid
	return sid, refresh, err
}

// failedSecuritySigner 仅在签发时失败，账号、会话和权限校验仍使用真实实现。
type failedSecuritySigner struct {
	JWTAccess                    // 校验适配
	failure   error              // 指定签发错误
	cancel    context.CancelFunc // 可选首次请求取消，用于核对失败清理的实际边界
}

// Sign 按夹具配置取消请求并返回签发故障，不生成令牌。
func (s failedSecuritySigner) Sign(profile operations.Profile, sid string, now time.Time) (string, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return "", s.failure
}

// TestSecuritySigningFailureCleanupBoundaries 验证签发失败可撤销真实会话，已取消请求下清理失败则仍受 TTL 限制。
// 取消分支不声称已注销，测试显式清理 SID；SQL 和 Redis 使用真实实现。
func TestSecuritySigningFailureCleanupBoundaries(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("canceled=", canceled), func(t *testing.T) {
			db, _, repo, account, password, jwt := securityFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tracked := &trackedSecuritySessions{SessionRepo: repo}
			failure := errors.New("signing fixture failure")
			signer := failedSecuritySigner{JWTAccess: JWTAccess{JWT: jwt}, failure: failure}
			if canceled {
				signer.cancel = cancel
			}
			service := SessionService{Accounts: AccountRepo{DB: db}, Sessions: tracked, Tokens: signer}
			reply, err := service.Login(ctx, account.Username, password, "fixture")
			if !errors.Is(err, failure) || reply.Access.AccessToken != "" || tracked.createdSID == "" {
				t.Fatalf("sign failure retained result %v", err)
			}
			t.Cleanup(func() {
				if err := repo.RevokeSession(context.Background(), tracked.createdSID); err != nil {
					t.Error(err)
				}
			})
			exists, err := repo.Exists(context.Background(), tracked.createdSID)
			if err != nil || exists != canceled {
				t.Fatalf("cleanup exists=%v canceled=%v err=%v", exists, canceled, err)
			}
		})
	}
}

// gatedChallenges 暂停两个调用，确保都从真实 Redis 读到同一挑战，再竞争两条 SQL 连接。
type gatedChallenges struct {
	SessionRepo                 // 真实会话和挑战删除，不用测试替身
	read        chan struct{}   // 每次读取完成通知
	release     <-chan struct{} // 两次读取后才放行
}

// ResolveMFA 读取真实挑战后发通知并等待放行，取消时返回错误。
func (r gatedChallenges) ResolveMFA(ctx context.Context, token string) (MFAChallenge, error) {
	value, err := r.SessionRepo.ResolveMFA(ctx, token)
	if err != nil {
		return value, err
	}
	r.read <- struct{}{}
	select {
	case <-r.release:
		return value, nil
	case <-ctx.Done():
		return MFAChallenge{}, ctx.Err()
	}
}

// TestSecurityConcurrentMFAChallengeOneSession 验证同一挑战并发验证只有一次删除、登录审计和会话创建；不保证先启动者获胜。
func TestSecurityConcurrentMFAChallengeOneSession(t *testing.T) {
	testConcurrentMFAConsumption(t, false)
}

// TestSecurityDifferentChallengesCannotReuseTOTPStep 验证挑战号不同仍共享账号密钥的时间步消费锁。
func TestSecurityDifferentChallengesCannotReuseTOTPStep(t *testing.T) {
	testConcurrentMFAConsumption(t, true)
}

// testConcurrentMFAConsumption 让两个独立连接同时完成挑战读取，再竞争同一账号行锁。
// Redis 删除次数不是时间步消费证据，另核对 SQL 登录审计、成功会话和剩余挑战数。
func testConcurrentMFAConsumption(t *testing.T, different bool) {
	first, second, repo, account, _, jwt := securityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{"mfa_enabled": true, "mfa_secret": secret}).Error; err != nil {
		t.Fatal(err)
	}
	challenge, err := repo.BeginMFA(ctx, account.Identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Redis.Del(context.Background(), mfaKey(challenge)) })
	challenges := []string{challenge, challenge}
	if different {
		challenges[1], err = repo.BeginMFA(ctx, account.Identity())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { repo.Redis.Del(context.Background(), mfaKey(challenges[1])) })
	}
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	read, release := make(chan struct{}, 2), make(chan struct{})
	gated := gatedChallenges{SessionRepo: repo, read: read, release: release}
	// result 汇总并发 MFA 返回值；挑战删除、SQL 审计和会话另行核对。
	type result struct {
		reply AccessReply // 本次挑战验证结果
		err   error       // 并发挑战验证错误
	}
	results := make(chan result, 2)
	for i, db := range []*gorm.DB{first, second} {
		go func() {
			s := SessionService{Accounts: AccountRepo{DB: db}, Sessions: gated, Tokens: JWTAccess{JWT: jwt}}
			reply, err := s.VerifyMFA(ctx, challenges[i], code, "concurrent")
			results <- result{reply, err}
		}()
	}
	for range 2 {
		select {
		case <-read:
		case <-ctx.Done():
			close(release)
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	winners := 0
	for range 2 {
		r := <-results
		if r.err == nil {
			winners++
			id, sid, err := (JWTAccess{JWT: jwt}).Verify(r.reply.AccessToken)
			if err != nil || id != account.ID {
				t.Fatalf("identity %d %v", id, err)
			}
			if err := repo.RevokeSession(context.Background(), sid); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(r.err, operations.ErrInvalidMFAChallenge) && !errors.Is(r.err, operations.ErrUsedTOTP) {
			t.Fatal(r.err)
		}
	}
	var count int64
	if err := first.Table("audit_log").Where("actor_id=? AND action='login_mfa'", account.ID).Count(&count).Error; err != nil || count != 1 || winners != 1 {
		t.Fatalf("winners=%d audits=%d err=%v", winners, count, err)
	}
	remaining := 0
	for _, token := range challenges {
		if _, err := repo.ResolveMFA(ctx, token); err == nil {
			remaining++
		} else if !errors.Is(err, operations.ErrInvalidMFAChallenge) {
			t.Fatal(err)
		}
	}
	want := 0
	if different {
		want = 1
	}
	if remaining != want {
		t.Fatalf("remaining challenges=%d want=%d", remaining, want)
	}
	if different {
		var current AdminAccount
		if err := first.Where("id=?", account.ID).Take(&current).Error; err != nil || current.MFALastStep == nil {
			t.Fatal("consumption not saved", err)
		}
		last, version := *current.MFALastStep, current.MFASecretVersion
		if err := UpdateAdminMFASecret(first, account.ID, &secret, true, true); err != nil {
			t.Fatal(err)
		}
		if err := first.Where("id=?", account.ID).Take(&current).Error; err != nil || current.MFALastStep == nil || *current.MFALastStep != last || current.MFASecretVersion != version {
			t.Fatal("authentication version reset consumption", err)
		}
		consumed := 0
		consume := func(context.Context) error { consumed++; return nil }
		if _, err := (AccountRepo{DB: second}).CompleteMFA(ctx, current.Identity(), code, "replay", consume); !errors.Is(err, operations.ErrUsedTOTP) || consumed != 0 {
			t.Fatalf("new auth version replay=%v consumed=%d", err, consumed)
		}
		next, err := auth.TOTPCode(secret, time.Unix((last+1)*30, 0))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (AccountRepo{DB: second}).CompleteMFA(ctx, current.Identity(), next, "next", consume); err != nil || consumed != 1 {
			t.Fatalf("next step=%v consumed=%d", err, consumed)
		}
		newSecret, err := auth.NewTOTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if err := UpdateAdminMFASecret(first, account.ID, &newSecret, true, false); err != nil {
			t.Fatal(err)
		}
		if err := first.Where("id=?", account.ID).Take(&current).Error; err != nil || current.MFALastStep != nil || current.MFASecretVersion != version+1 {
			t.Fatal("new secret consumption not reset", err)
		}
	}
}

// TestSecurityMFASQLFailureAfterConsumeClosesLogin 验证挑战已删除后审计失败，SQL 回滚且不签发会话。
// Redis 挑战无法恢复，重试须重新密码登录；注入故障不代表真实提交结果未知。
func TestSecurityMFASQLFailureAfterConsumeClosesLogin(t *testing.T) {
	db, _, repo, account, _, jwt := securityFixture(t)
	ctx := context.Background()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{"mfa_enabled": true, "mfa_secret": secret, "failed_login_count": 2}).Error; err != nil {
		t.Fatal(err)
	}
	challenge, err := repo.BeginMFA(ctx, account.Identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Redis.Del(ctx, mfaKey(challenge)) })
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("audit fixture failure")
	name := fmt.Sprint("security-audit-", account.ID)
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	service := SessionService{Accounts: AccountRepo{DB: db}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
	reply, err := service.VerifyMFA(ctx, challenge, code, "fixture")
	db.Callback().Create().Remove(name)
	if !errors.Is(err, failure) || reply.AccessToken != "" {
		t.Fatalf("audit failure reply %+v %v", reply, err)
	}
	var actual AdminAccount
	if err := db.Where("id=?", account.ID).Take(&actual).Error; err != nil || actual.FailedLoginCount != 2 {
		t.Fatalf("rollback %+v %v", actual, err)
	}
	if _, err := repo.ResolveMFA(ctx, challenge); !errors.Is(err, operations.ErrInvalidMFAChallenge) {
		t.Fatalf("challenge restored %v", err)
	}
}
