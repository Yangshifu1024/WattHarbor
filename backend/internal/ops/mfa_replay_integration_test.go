//go:build integration

package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"wattharbor/internal/domain/operations"
	"wattharbor/internal/platform/auth"
)

// TestUsedTOTPDoesNotLockAccount 使用真实SQL/Redis挑战重复提交已消费正确码，保持已有4次坏码计数。
// 核对无新会话、挑战保留、失败审计不增加；随后下一步成功，真正坏码仍在第五次锁定。
func TestUsedTOTPDoesNotLockAccount(t *testing.T) {
	db, _, sessions, account, _, jwt := securityFixture(t)
	ctx := context.Background()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step, err := auth.VerifyTOTP(secret, code, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Table("admin_user").Where("id=?", account.ID).Updates(map[string]any{
		"mfa_enabled": true, "mfa_secret": secret, "mfa_last_step": step, "failed_login_count": 4,
	}).Error; err != nil {
		t.Fatal(err)
	}
	challenge, err := sessions.BeginMFA(ctx, account.Identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessions.Redis.Del(ctx, mfaKey(challenge)) })
	service := SessionService{Accounts: AccountRepo{DB: db}, Sessions: sessions, Tokens: JWTAccess{JWT: jwt}}
	for range 6 {
		reply, err := service.VerifyMFA(ctx, challenge, code, "repeated-valid-code")
		if !errors.Is(err, operations.ErrUsedTOTP) || reply.AccessToken != "" {
			t.Fatalf("replay issued access=%t err=%v", reply.AccessToken != "", err)
		}
	}
	var current AdminAccount
	if err := db.Where("id=?", account.ID).Take(&current).Error; err != nil || current.FailedLoginCount != 4 || current.Status != "active" || current.LockedUntil.Valid || current.MFALastStep == nil || *current.MFALastStep != step {
		t.Fatalf("replay changed security state: failed=%d status=%s locked=%t err=%v", current.FailedLoginCount, current.Status, current.LockedUntil.Valid, err)
	}
	if _, err := sessions.ResolveMFA(ctx, challenge); err != nil {
		t.Fatal("replay consumed challenge", err)
	}
	var failed int64
	if err := db.Table("audit_log").Where("actor_id=? AND action='mfa_failed'", account.ID).Count(&failed).Error; err != nil || failed != 0 {
		t.Fatalf("failed audit=%d err=%v", failed, err)
	}
	next, err := auth.TOTPCode(secret, time.Unix((step+1)*30, 0))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := service.VerifyMFA(ctx, challenge, next, "next-code")
	if err != nil || reply.AccessToken == "" {
		t.Fatalf("next step issued access=%t err=%v", reply.AccessToken != "", err)
	}
	_, sid, err := (JWTAccess{JWT: jwt}).Verify(reply.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessions.RevokeSession(ctx, sid) })
	badChallenge, err := sessions.BeginMFA(ctx, account.Identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessions.Redis.Del(ctx, mfaKey(badChallenge)) })
	// 新一步已消费，旧码仍处在可接受窗口内；拒绝更早正确步也不算坏码。
	if _, err := service.VerifyMFA(ctx, badChallenge, code, "earlier-valid-code"); !errors.Is(err, operations.ErrUsedTOTP) {
		t.Fatalf("earlier valid step: %v", err)
	}
	for i := 1; i <= 5; i++ {
		_, err := service.VerifyMFA(ctx, badChallenge, "abcdef", "invalid-code")
		want := operations.ErrInvalidTOTP
		if i == 5 {
			want = operations.ErrLocked
		}
		if !errors.Is(err, want) {
			t.Fatalf("bad attempt %d: %v", i, err)
		}
	}
	if err := db.Where("id=?", account.ID).Take(&current).Error; err != nil || current.FailedLoginCount != 5 || current.Status != "locked" || !current.LockedUntil.Valid {
		t.Fatalf("bad codes did not lock: failed=%d status=%s locked=%t err=%v", current.FailedLoginCount, current.Status, current.LockedUntil.Valid, err)
	}
}
