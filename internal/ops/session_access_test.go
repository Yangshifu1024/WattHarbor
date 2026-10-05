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
		Data struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
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
		name  string
		at    int64
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
		Data operations.Profile `json:"data"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil || reply.Code != 200 || envelope.Data.DisplayName != "管理员显示名" || envelope.Data.RoleName != "自定义中文角色" || envelope.Data.RoleID != 8 || !envelope.Data.MFAEnabled {
		t.Fatalf("issued identity omitted live account metadata: status=%d err=%v", reply.Code, err)
	}
}
