package ops

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIssuedAdminAccessTokenLastsEightHours 使用 t 经真实签发、HTTP 信封与 JWT 验证核对八小时有效期、别名和账号／会话身份；无返回值，失败由 t 报告，不访问数据库、Redis 或执行登录鉴权流程。
func TestIssuedAdminAccessTokenLastsEightHours(t *testing.T) {
	jwt, err := auth.NewJWT(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	router.GET("/issue", func(c *gin.Context) {
		result, err := (SessionService{Tokens: JWTAccess{JWT: jwt}}).issue(c.Request.Context(), operations.Profile{ID: 7, RoleID: 8}, "fixture-session", "fixture-refresh")
		if err != nil {
			t.Fatal(err)
		}
		httpapi.OK(c, result)
	})
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, "/issue", nil))
	if reply.Code != http.StatusOK {
		t.Fatalf("issue status = %d", reply.Code)
	}
	var envelope struct {
		// Data 签发信封中的访问令牌与期限投影，仅用于 JSON 形状和 JWT 一致性比对。
		Data struct {
			// Token 签发回复的原 token 别名，预期非空且与 AccessToken 相同。
			Token string `json:"token"`
			// AccessToken 签发回复的访问 JWT，携管理员、会话及八小时期限，不是刷新令牌。
			AccessToken string `json:"access_token"`
			// ExpiresIn 签发回复的有效期，单位秒，与 JWT 的到期秒减签发秒比对。
			ExpiresIn int64 `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Token == "" || envelope.Data.AccessToken != envelope.Data.Token {
		t.Fatal("access token aliases differ or are empty")
	}
	claims, err := jwt.Verify(envelope.Data.AccessToken, "admin", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const lifetimeSeconds = int64(8 * time.Hour / time.Second)
	if lifetime := claims.ExpiresAt - claims.IssuedAt; lifetime != lifetimeSeconds || envelope.Data.ExpiresIn != lifetime {
		t.Fatalf("JWT lifetime = %d, expires_in = %d, want %d", lifetime, envelope.Data.ExpiresIn, lifetimeSeconds)
	}
	if claims.Subject != "7" || claims.SessionID != "fixture-session" {
		t.Fatal("issued token lost account or session identity")
	}
	for _, tc := range []struct {
		// name 当前边界场景名称，供 t.Run 区分断言；不作为业务身份或数据库条件。
		name string
		// at JWT 验证所用 Unix 时刻，单位秒，覆盖八小时期限之前、边界及之后。
		at int64
		// valid 该时刻预期的 JWT 有效性，仅验证签名及期限，不验证 Redis 会话或数据库权限。
		valid bool
	}{
		{"after_previous_expiry", claims.IssuedAt + 16*60, true},
		{"before_eight_hours", claims.ExpiresAt - 1, true},
		{"at_eight_hours", claims.ExpiresAt, false},
		{"after_eight_hours", claims.ExpiresAt + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jwt.Verify(envelope.Data.AccessToken, "admin", time.Unix(tc.at, 0))
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("valid = %t, verification error = %v", tc.valid, err)
			}
		})
	}
}

// TestIssuedIdentityIncludesDisplayNameActualRoleAndMFAState 使用 t 核对签发回复保留传入的显示名、角色名称、角色主键及 MFA 状态；无返回值，失败由 t 报告，账号资料由夹具给定，不证明实时数据库权限查询。
func TestIssuedIdentityIncludesDisplayNameActualRoleAndMFAState(t *testing.T) {
	jwt, err := auth.NewJWT(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	a := SessionService{Tokens: JWTAccess{JWT: jwt}}
	router := httpapi.NewRouter()
	router.GET("/identity", func(c *gin.Context) {
		result, err := a.issue(c.Request.Context(), operations.Profile{ID: 7, Username: "fixture", DisplayName: "管理员显示名", Role: "custom", RoleName: "自定义中文角色", RoleID: 8, MFAEnabled: true}, "fixture-session", "fixture-refresh")
		if err != nil {
			t.Fatal(err)
		}
		httpapi.OK(c, result)
	})
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, "/identity", nil))
	var envelope struct {
		// Data 签发信封中回显的账号资料；来自本测试提供的 Profile，不证明实时账号查询。
		Data operations.Profile `json:"data"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil || reply.Code != 200 || envelope.Data.DisplayName != "管理员显示名" || envelope.Data.RoleName != "自定义中文角色" || envelope.Data.RoleID != 8 || !envelope.Data.MFAEnabled {
		t.Fatalf("issued identity omitted live account metadata: status=%d err=%v", reply.Code, err)
	}
}
