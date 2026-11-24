package charging

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gatewaycontract "wattharbor/internal/contracts/gateway"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// liveSession 让测试会话始终有效，JWT 身份仍由真实认证器解析。
type liveSession struct{}

// Exists 返回 true，跳过 Redis 会话读取。
func (liveSession) Exists(context.Context, string) (bool, error) { return true, nil }

// liveUser 让测试用户始终有效。
type liveUser struct{}

// Active 返回 true，跳过用户状态查询。
func (liveUser) Active(context.Context, uint64) (bool, error) { return true, nil }

func TestUserStopUsesJWTIdentityAndDoesNotClaimPowerOff(t *testing.T) {
	jwt, err := auth.NewJWT("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	token, err := jwt.Sign(auth.Claims{Subject: "9007199254740993", Kind: "user", OpenID: "wx-test", SessionID: "sid", IssuedAt: now, ExpiresAt: now + 600})
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrderNo string `json:"order_no"`
			UserID  uint64 `json:"user_id"`
			Source  string `json:"source"`
		}
		if r.Header.Get("X-Service-Token") != "secret" || json.NewDecoder(r.Body).Decode(&body) != nil || body.OrderNo != "ORD-123" || body.UserID != 9007199254740993 || body.Source != "user_app" {
			t.Errorf("bad gateway request: %+v", body)
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0,"data":{"accepted":true,"stopped":false,"command_id":"abc","status":"sent"}}`))
	}))
	defer gateway.Close()
	router := httpapi.NewRouter()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: gateway.Client()}, gateway.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	StopAPI{JWT: jwt, Sessions: liveSession{}, Users: liveUser{}, Gateway: client}.Register(router)
	request := func(authHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/charge/stop", strings.NewReader(`{"order_no":"ORD-123","user_id":42,"source":"auto"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}
	if response := request(""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized stop: %d", response.Code)
	}
	response := request("Bearer " + token)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stopped":false`) {
		t.Fatalf("stop response: %d %s", response.Code, response.Body.String())
	}
}

// failingStopGateway 返回预设停止请求错误。
type failingStopGateway struct {
	err error
}

// RequestStop 返回空回复和预设错误。
func (s failingStopGateway) RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
	return gatewaycontract.StopReply{}, s.err
}

func TestUserStopPreservesGatewayFailureMapping(t *testing.T) {
	jwt, err := auth.NewJWT("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	token, err := jwt.Sign(auth.Claims{Subject: "42", Kind: "user", OpenID: "wx-test", SessionID: "sid", IssuedAt: now, ExpiresAt: now + 600})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		err  error
		// status,code 预期的 HTTP 状态与业务错误码。
		status, code int
	}{
		{"conflict", &serviceclient.HTTPError{StatusCode: 409}, 409, 2000},
		{"not found", &serviceclient.HTTPError{StatusCode: 404}, 502, 5003},
		{"service token", &serviceclient.HTTPError{StatusCode: 401}, 502, 5003},
		{"upstream unavailable", &serviceclient.HTTPError{StatusCode: 503}, 502, 5003},
		{"invalid response", gatewaycontract.ErrInvalidResponse, 502, 5003},
		{"business failure", gatewaycontract.BusinessError{Code: 2000}, 502, 5003},
		{"network failure", errors.New("private network error"), 503, 5001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := httpapi.NewRouter()
			StopAPI{JWT: jwt, Sessions: liveSession{}, Users: liveUser{}, Gateway: failingStopGateway{err: tc.err}}.Register(router)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/user/charge/stop", strings.NewReader(`{"order_no":"ORD-123"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			var reply struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != tc.status || reply.Code != tc.code || strings.Contains(w.Body.String(), "private network") {
				t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
			}
		})
	}
}
