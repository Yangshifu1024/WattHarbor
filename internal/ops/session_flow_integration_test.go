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

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// securityFixture 为t打开独立真实连接池和Redis，提交自己的自定义角色／账号；不影响bootstrap管理员。
// 返回两独立连接、会话repo、账号、口令及JWT；提交造数清理自身行和认证缓存并核对零残留。
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
	id := uint64(time.Now().UnixNano())
	password := "Test!SecurityPassword2026"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Table("role").Create(map[string]any{"id": id + 1, "code": "security-" + tag, "name": "security fixture", "is_builtin": false}).Error; err != nil {
		t.Fatal(err)
	}
	account := AdminAccount{ID: id, Username: "security-" + tag, PasswordHash: string(hash), RoleID: id + 1, Status: "active", AuthVersion: 9007199254741111}
	if err := first.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"audit_log", "admin_user", "role_permission", "role"} {
			column, value := "id", account.ID
			switch table {
			case "audit_log":
				column = "actor_id"
			case "role_permission":
				column = "role_id"
				value = account.RoleID
			case "role":
				value = account.RoleID
			}
			if err := first.Table(table).Where(column+"=?", value).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var count int64
			if err := first.Table(table).Where(column+"=?", value).Count(&count).Error; err != nil || count != 0 {
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

// TestSecuritySessionLiveVersionAndMFAAdmission 使用真实SQL与Redis验证即时撤权、改密、挑战版本和锁定准入。
// 不消费坏码／锁定／旧版本挑战；成功消费后重放拒绝，所有会话按本测试SID清理。
func TestSecuritySessionLiveVersionAndMFAAdmission(t *testing.T) {
	db, _, repo, account, password, jwt := securityFixture(t)
	ctx := context.Background()
	service := SessionService{Accounts: AdminStore{DB: db}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
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

// TestSecurityMFAChallengeVersionUsesLockedCurrentRead 固定旧RR快照早于另一连接提交版本提升，锁后必须拒绝旧挑战。
// 版本变更真实提交，测试会话／挑战和SQL造数均只清理自身身份，不把普通快照读当实时准入。
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
	service := SessionService{Accounts: AdminStore{DB: tx}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
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

// trackedSecuritySessions 保留真实存储并记录本次新SID，供签发失败清理核对。
type trackedSecuritySessions struct {
	SessionRepo        // 原创建／轮换／撤销行为
	createdSID  string // 原创建返回的唯一SID
}

// Create 在ctx下为account真实创建并记住SID，返回原结果或存储错误，不替换撤销语义。
func (s *trackedSecuritySessions) Create(ctx context.Context, account operations.AccountIdentity) (string, string, error) {
	sid, refresh, err := s.SessionRepo.Create(ctx, account)
	s.createdSID = sid
	return sid, refresh, err
}

// failedSecuritySigner 只注入签发阶段失败；会话、SQL与校验均为真实实现。
type failedSecuritySigner struct {
	JWTAccess                    // 原校验适配
	failure   error              // 指定签发错误
	cancel    context.CancelFunc // 可选原请求取消，用于核对失败清理的实际边界
}

// Sign 忽略profile／sid／now的签发并按fixture取消原请求，返回failure，不生成伪令牌。
func (s failedSecuritySigner) Sign(profile operations.Profile, sid string, now time.Time) (string, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return "", s.failure
}

// TestSecuritySigningFailureCleanupBoundaries 验证签发失败真实撤销，以及取消原context后清理失败的实际TTL边界。
// 取消分支不宣称已注销，由本测试显式清理随机SID；签发器为fixture，SQL／Redis为真实实现。
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
			service := SessionService{Accounts: AdminStore{DB: db}, Sessions: tracked, Tokens: signer}
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

// gatedChallenges 在真实Redis原读取后闸住两调用，保证双方持有同一原挑战再竞争两个SQL连接。
type gatedChallenges struct {
	SessionRepo                 // 实际会话与消费，不以fixture替换原子性
	read        chan struct{}   // 每次原读取完成通知
	release     <-chan struct{} // 两原读取后才放行
}

// ResolveMFA 在ctx下真实读取token后通知read并等待release，返回原凭据或取消错误，不伪造身份。
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

// TestSecurityConcurrentMFAChallengeOneSession 固定两真实Redis原读取及两SQL连接，只有一个消费／成功审计／会话。
// 锁后旧凭据不得重放，不要求先启动的请求获胜；提交造数按自身身份清理。
func TestSecurityConcurrentMFAChallengeOneSession(t *testing.T) {
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
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	read, release := make(chan struct{}, 2), make(chan struct{})
	gated := gatedChallenges{SessionRepo: repo, read: read, release: release}
	type result struct {
		reply AccessReply
		err   error
	}
	results := make(chan result, 2)
	for _, db := range []*gorm.DB{first, second} {
		go func() {
			s := SessionService{Accounts: AdminStore{DB: db}, Sessions: gated, Tokens: JWTAccess{JWT: jwt}}
			reply, err := s.VerifyMFA(ctx, challenge, code, "concurrent")
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
		} else if !errors.Is(r.err, operations.ErrInvalidMFAChallenge) {
			t.Fatal(r.err)
		}
	}
	var count int64
	if err := first.Table("audit_log").Where("actor_id=? AND action='login_mfa'", account.ID).Count(&count).Error; err != nil || count != 1 || winners != 1 {
		t.Fatalf("winners=%d audits=%d err=%v", winners, count, err)
	}
	if _, err := repo.ResolveMFA(ctx, challenge); !errors.Is(err, operations.ErrInvalidMFAChallenge) {
		t.Fatalf("consumed challenge retained %v", err)
	}
}

// TestSecurityMFASQLFailureAfterConsumeClosesLogin 真实消费后注入审计故障，SQL状态回滚且不签发会话。
// 原挑战保持已消费，重试须重新密码登录；fixture故障不等于实际数据库提交未知证据。
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
	service := SessionService{Accounts: AdminStore{DB: db}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
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
