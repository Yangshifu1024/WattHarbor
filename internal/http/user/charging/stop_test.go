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

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

type liveSession struct{}

// Exists 忽略输入 context 和 SID，返回 true、nil，供停止路由隔离会话依赖；不访问 Redis。
func (liveSession) Exists(context.Context, string) (bool, error) { return true, nil }

type liveUser struct{}

// Active 忽略输入 context 和用户编号，返回 true、nil，供停止路由隔离用户依赖；不访问数据库。
func (liveUser) Active(context.Context, uint64) (bool, error) { return true, nil }

// TestUserStopUsesJWTIdentityAndDoesNotClaimPowerOff 用 t 验证真实 JWT 和注册路由，将用户身份传给测试 HTTP gateway 并核对受理形状；会话与用户状态为固定 fixture，不证明设备已停电。
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
			// OrderNo 捕获的停止请求业务订单号。
			OrderNo string `json:"order_no"`
			// UserID 捕获的已认证用户编号。
			UserID uint64 `json:"user_id"`
			// Source 捕获的停止请求来源。
			Source string `json:"source"`
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

type failingStopGateway struct {
	// err 指定的 gateway 错误 fixture，不访问真实设备。
	err error
}

// RequestStop 忽略 context 和停止请求，返回空回执及 s.err，用于错误映射断言；不连接 gateway 或设备。
func (s failingStopGateway) RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
	return gatewaycontract.StopReply{}, s.err
}

// TestUserStopPreservesGatewayFailureMapping 用 t 验证注册路由对窄 gateway fixture 的各类失败映射；无真库、设备或网络投递。
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
		// name 该子测试的场景名称。
		name string
		// err 指定的 gateway 错误 fixture，不访问真实设备。
		err error
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
				// Code 本断言核对的响应业务码或 MFA 验证码。
				Code int `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != tc.status || reply.Code != tc.code || strings.Contains(w.Body.String(), "private network") {
				t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
			}
		})
	}
}
