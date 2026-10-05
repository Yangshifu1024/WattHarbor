package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	userhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

// deniedUserIdentity 提供指定认证结果的窄能力fixture，不模拟业务查询或写入。
// 真实SessionAuthenticator和identity.Authenticator仍执行凭据、会话、用户状态的原顺序。
// 空mode提供用户17的有效身份，供其它注册接口测试核对所属业务IO。
type deniedUserIdentity struct {
	mode string // 仅控制指定的签名、会话或用户状态失败；不签发真实JWT，不连接数据库或Redis。
}

// Verify 只接受测试opaque令牌，返回原用户与SID，或mode指定的坏签名／零身份。
func (f deniedUserIdentity) Verify(token string) (string, string, error) {
	if token != "opaque" || f.mode == "signature" {
		return "", "", errors.New("private-auth-source-error")
	}
	if f.mode == "zero-subject" {
		return "0", "sid", nil
	}
	return "17", "sid", nil
}

// Exists 在ctx下检查fixture的sid，返回过期false或读取／取消错误；不修改会话。
func (f deniedUserIdentity) Exists(ctx context.Context, sid string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.mode == "session-error" || sid != "sid" {
		return false, errors.New("private-auth-source-error")
	}
	return f.mode != "expired", nil
}

// Active 在ctx下检查fixture原用户id，返回冻结false或读取／取消错误；不执行业务IO。
func (f deniedUserIdentity) Active(ctx context.Context, id uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.mode == "user-error" || id != 17 {
		return false, errors.New("private-auth-source-error")
	}
	return f.mode != "frozen", nil
}

// TestRegisteredUserAuthenticationFailureWritesOneReply 验证29个实际注册的受保护用户端点。
// 各类认证失败只输出一个原错误信封，保留401／403／503、请求号与无敏感原因；业务依赖未装配，不能继续执行。
func TestRegisteredUserAuthenticationFailureWritesOneReply(t *testing.T) {
	endpoints := []struct {
		method string // 原公开HTTP方法。
		path   string // 含具体用户资源身份的公开路径；其业务依赖刻意未装配。
	}{
		{"GET", "/api/v1/user/wallet/balance"},
		{"GET", "/api/v1/user/wallet/txns"},
		{"GET", "/api/v1/user/wallet/recharges"},
		{"POST", "/api/v1/user/wallet/recharge"},
		{"GET", "/api/v1/user/wallet/refunds"},
		{"POST", "/api/v1/user/wallet/refund"},
		{"GET", "/api/v1/user/coupon/my"},
		{"POST", "/api/v1/user/device/report-fault"},
		{"GET", "/api/v1/user/device/fault-reports"},
		{"GET", "/api/v1/user/device/fault-reports/1/history"},
		{"GET", "/api/v1/user/invoice/my"},
		{"POST", "/api/v1/user/invoice/apply"},
		{"POST", "/api/v1/user/charge/1/feedback"},
		{"GET", "/api/v1/user/charge/ongoing"},
		{"GET", "/api/v1/user/charge/ongoing/snapshot"},
		{"GET", "/api/v1/user/charge/ongoing/curve"},
		{"GET", "/api/v1/user/charge/history"},
		{"GET", "/api/v1/user/charge/C1/curve"},
		{"GET", "/api/v1/user/charge/C1"},
		{"GET", "/api/v1/user/bills"},
		{"GET", "/api/v1/user/bills/1"},
		{"POST", "/api/v1/user/bills/1/read"},
		{"GET", "/api/v1/user/coupons"},
		{"GET", "/api/v1/user/cards"},
		{"POST", "/api/v1/user/cards/1/status"},
		{"POST", "/api/v1/user/phone/bind"},
		{"POST", "/api/v1/user/phone/unbind"},
		{"POST", "/api/v1/user/scan/start"},
		{"POST", "/api/v1/user/development/payments/confirm"},
	}
	for _, failure := range []struct {
		mode   string // 认证失败来源；missing和malformed控制原请求头。
		status int    // identity错误对应的原HTTP状态。
		code   int    // 原公开业务错误码。
	}{
		{"missing", 401, 1001}, {"malformed", 401, 1001}, {"signature", 401, 1001},
		{"zero-subject", 401, 1001}, {"expired", 401, 1001}, {"frozen", 403, 1003},
		{"session-error", 503, 5001}, {"user-error", 503, 5001}, {"cancelled", 503, 5001},
	} {
		t.Run(failure.mode, func(t *testing.T) {
			fixture := deniedUserIdentity{mode: failure.mode}
			a := authhttp.SessionAuthenticator{Auth: identity.Authenticator{Tokens: fixture, Sessions: fixture, Users: fixture}}
			r := httpapi.NewRouter()
			userhttp.UserAccountAPI{Auth: a}.Register(r)
			userhttp.UserQueryAPI{Auth: a}.Register(r)
			userhttp.BillHTTP{Auth: a}.Register(r)
			userhttp.CouponAPI{Auth: a}.Register(r)
			userhttp.CardAPI{Auth: a}.RegisterUser(r)
			userhttp.PhoneAPI{Auth: a}.Register(r)
			userhttp.PaymentStartAPI{Auth: a}.Register(r)
			userhttp.DevelopmentPaymentAPI{Auth: a}.Register(r)
			for _, endpoint := range endpoints {
				t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
					req := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`))
					req.Header.Set("X-Request-ID", "single-auth-reply")
					if failure.mode != "missing" {
						req.Header.Set("Authorization", "Bearer opaque")
					}
					if failure.mode == "malformed" {
						req.Header.Set("Authorization", "Bearer opaque extra")
					}
					if failure.mode == "cancelled" {
						ctx, cancel := context.WithCancel(req.Context())
						cancel()
						req = req.WithContext(ctx)
					}
					w := httptest.NewRecorder()
					r.ServeHTTP(w, req)
					if w.Code != failure.status {
						t.Fatalf("status=%d want=%d: %s", w.Code, failure.status, w.Body.String())
					}
					var reply httpapi.Envelope
					d := json.NewDecoder(w.Body)
					if err := d.Decode(&reply); err != nil {
						t.Fatal(err)
					}
					if reply.Code != failure.code || reply.Data != nil || reply.RequestID != "single-auth-reply" || reply.TraceID != reply.RequestID || strings.Contains(reply.Message, "private-auth-source-error") {
						t.Fatalf("invalid auth reply: %+v", reply)
					}
					if err := d.Decode(new(any)); err != io.EOF {
						t.Fatalf("authentication must write exactly one JSON reply, second decode=%v", err)
					}
				})
			}
		})
	}
}
