//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	authhttp "wattharbor/internal/http/auth"
	useridentity "wattharbor/internal/http/user/identity"
	"wattharbor/internal/identity"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/sessiontoken"
)

// identityRouteRedis 为 t 打开真实测试 Redis，缺配置或连接失败终止；只关闭本连接，不清空数据。
func identityRouteRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	return client
}

// observedUserSessions 仅记录真实 repo 产生的 SID 供清理断言，所有会话 IO 仍交真实 Redis。
type observedUserSessions struct {
	identity.SessionRepo          // 真实用户会话实现
	sids                 []string // 本测试已创建的 SID，不含令牌明文
}

// Create 为 user 调用真实实现，成功记录 SID，返回结果或存储错误。
func (s *observedUserSessions) Create(ctx context.Context, user identity.User) (string, string, error) {
	sid, token, err := s.SessionRepo.Create(ctx, user)
	if err == nil {
		s.sids = append(s.sids, sid)
	}
	return sid, token, err
}

// TestRegisteredIdentityRoutesUseRealUserWalletAndRedis 验证实际注册登录、轮换、注销、档案及正式号码端点。
// 微信交换是 fixture；用户、钱包和会话均为真实实现，数据库造数回滚，Redis 只清理本测试 SID。
func TestRegisteredIdentityRoutesUseRealUserWalletAndRedis(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	client := identityRouteRedis(t)
	sessions := &observedUserSessions{SessionRepo: identity.SessionRepo{Redis: client}}
	layout := sessiontoken.Layout{TokenPrefix: "RT_", KeyPrefix: "user:session:"}
	t.Cleanup(func() {
		for _, sid := range sessions.sids {
			if err := client.Del(context.Background(), layout.Key(sid)).Err(); err != nil {
				t.Error(err)
			}
		}
	})
	jwt, err := auth.NewJWT("registered-identity-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	users := identity.UserRepo{DB: tx}
	service := identity.SessionService{WeChat: identity.DevelopmentExchanger{}, Users: users, Sessions: sessions, Tokens: identity.JWTAccess{JWT: jwt}}
	router := httpapi.NewRouter()
	(useridentity.API{Service: service}).Register(router)
	exchanges := 0
	number := fmt.Sprintf("199%08d", time.Now().UnixNano()%100000000)
	source := identity.WeChatPhoneSource{Exchange: func(ctx context.Context, code string) (string, error) {
		exchanges++
		if code != "verified-code" {
			return "", errors.New("fixture exchange denied")
		}
		return number, ctx.Err()
	}}
	(useridentity.PhoneAPI{Auth: authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, sessions, users)}, Service: identity.PhoneService{Users: users, Source: source}}).Register(router)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		var envelope struct {
			Code int             `json:"code"` /* 业务状态 */
			Data json.RawMessage `json:"data"` /* 公开结果 */
		}
		if w.Code != 200 {
			t.Fatalf("HTTP=%d", w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Code != 0 {
			t.Fatalf("envelope code=%d err=%v", envelope.Code, err)
		}
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			t.Fatal(err)
		}
	}
	account := "identity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	body := fmt.Sprintf("{\"code\":\"dev:%s\"}", account)
	if w := call("POST", "/api/v1/user/auth/login", "{}", ""); w.Code != 400 {
		t.Fatalf("empty login=%d", w.Code)
	}
	var first, again identity.LoginResult
	decode(call("POST", "/api/v1/user/auth/login", body, ""), &first)
	decode(call("POST", "/api/v1/user/auth/login", body, ""), &again)
	id, err := strconv.ParseUint(first.UserID, 10, 64)
	if err != nil || id == 0 || !first.IsNewUser || again.IsNewUser || again.UserID != first.UserID || first.JWT != first.Token || first.JWTExpiresIn != 900 {
		t.Fatal("login identity or original response changed")
	}
	var accounts, wallets int64
	if err := tx.Table("user").Where("openid=?", "development:"+account).Count(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("wallet_account").Where("user_id=?", id).Count(&wallets).Error; err != nil || accounts != 1 || wallets != 1 {
		t.Fatalf("opening users=%d wallets=%d err=%v", accounts, wallets, err)
	}
	var profile identity.Profile
	decode(call("GET", "/api/v1/user/profile", "", first.Token), &profile)
	if profile.UserID != id || profile.PhoneBound || profile.Wallet.AvailableCents != 0 {
		t.Fatal("initial profile changed")
	}
	for _, invalid := range []string{"{}", fmt.Sprintf("{\"phone\":\"%s\"}", number), fmt.Sprintf("{\"code\":\"verified-code\",\"phone\":\"%s\"}", number)} {
		if w := call("POST", "/api/v1/user/phone/bind", invalid, first.Token); w.Code != 400 {
			t.Fatalf("formal input=%d", w.Code)
		}
	}
	if exchanges != 0 {
		t.Fatal("invalid formal input reached exchange")
	}
	if w := call("POST", "/api/v1/user/phone/bind", "{\"code\":\"bad-code\"}", first.Token); w.Code != 400 {
		t.Fatalf("failed exchange=%d", w.Code)
	}
	var bound identity.PhoneResult
	decode(call("POST", "/api/v1/user/phone/bind", "{\"code\":\"verified-code\"}", first.Token), &bound)
	if !bound.Bound || bound.PhoneMasked == number {
		t.Fatal("phone result leaked or changed")
	}
	decode(call("GET", "/api/v1/user/profile", "", first.Token), &profile)
	if !profile.PhoneBound {
		t.Fatal("phone was not committed")
	}
	if w := call("POST", "/api/v1/user/phone/unbind", "", first.Token); w.Code != 200 {
		t.Fatalf("unbind=%d", w.Code)
	}
	var phone *string
	if err := tx.Table("user").Where("id=?", id).Select("phone").Scan(&phone).Error; err != nil || phone != nil {
		t.Fatalf("unbind NULL=%v err=%v", phone == nil, err)
	}
	var refreshed identity.AccessResult
	decode(call("POST", "/api/v1/user/auth/refresh", "", first.RefreshToken), &refreshed)
	if refreshed.RefreshToken == first.RefreshToken || refreshed.JWT != refreshed.Token || refreshed.JWTExpiresIn != 900 {
		t.Fatal("rotation response changed")
	}
	if w := call("POST", "/api/v1/user/auth/refresh", "", first.RefreshToken); w.Code != 401 {
		t.Fatalf("repeated refresh=%d", w.Code)
	}
	if w := call("POST", "/api/v1/user/auth/logout", "", first.RefreshToken); w.Code != 200 {
		t.Fatalf("previous token logout=%d", w.Code)
	}
	if w := call("GET", "/api/v1/user/profile", "", refreshed.Token); w.Code != 401 {
		t.Fatalf("revoked access=%d", w.Code)
	}
	if err := tx.Table("user").Where("id=?", id).Update("status", "frozen").Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/user/profile", "", again.Token); w.Code != 403 {
		t.Fatalf("frozen profile=%d", w.Code)
	}
	if w := call("POST", "/api/v1/user/phone/unbind", "", again.Token); w.Code != 403 {
		t.Fatalf("frozen phone=%d", w.Code)
	}
	if w := call("POST", "/api/v1/user/auth/login", body, ""); w.Code != 403 {
		t.Fatalf("frozen login=%d", w.Code)
	}
	if w := call("POST", "/api/v1/user/auth/refresh", "", again.RefreshToken); w.Code != 403 {
		t.Fatalf("frozen refresh=%d", w.Code)
	}
	if exists, err := sessions.Exists(context.Background(), sessions.sids[1]); err != nil || exists {
		t.Fatal("frozen refresh did not revoke original session")
	}
	if err := tx.Table("user").Where("id=?", id).Update("status", "active").Error; err != nil {
		t.Fatal(err)
	}
	// 签发故障仅替换令牌能力；真实开户与会话创建仍执行，失败必须清理刚创建会话。
	broken := service
	broken.Tokens = identity.JWTAccess{}
	if _, err := broken.Login(context.Background(), "dev:"+account); err == nil {
		t.Fatal("missing signer accepted")
	}
	last := sessions.sids[len(sessions.sids)-1]
	if exists, err := sessions.Exists(context.Background(), last); err != nil || exists {
		t.Fatal("sign failure left session")
	}
	valid, err := service.Login(context.Background(), "dev:"+account)
	if err != nil {
		t.Fatal(err)
	}
	last = sessions.sids[len(sessions.sids)-1]
	if _, err := broken.Refresh(context.Background(), valid.RefreshToken); err == nil {
		t.Fatal("missing refresh signer accepted")
	}
	if exists, err := sessions.Exists(context.Background(), last); err != nil || exists {
		t.Fatal("refresh sign failure left session")
	}
	// 损坏记录模拟真实 Redis Lua 错误，公开端点须回存储故障而非凭据失效。
	decode(call("POST", "/api/v1/user/auth/login", body, ""), &again)
	last = sessions.sids[len(sessions.sids)-1]
	if err := client.Set(context.Background(), layout.Key(last), "{bad-json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/v1/user/auth/refresh", "", again.RefreshToken); w.Code != 503 || !strings.Contains(w.Body.String(), "session storage unavailable") {
		t.Fatalf("Redis fault HTTP=%d", w.Code)
	}
}
