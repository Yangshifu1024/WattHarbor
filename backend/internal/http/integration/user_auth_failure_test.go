package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	authhttp "wattharbor/internal/http/auth"
	usercard "wattharbor/internal/http/user/card"
	userdevice "wattharbor/internal/http/user/device"
	useridentity "wattharbor/internal/http/user/identity"
	usermarketing "wattharbor/internal/http/user/marketing"
	userorder "wattharbor/internal/http/user/order"
	userpayment "wattharbor/internal/http/user/payment"
	usersettlement "wattharbor/internal/http/user/settlement"
	userwallet "wattharbor/internal/http/user/wallet"
	"wattharbor/internal/identity"
	"wattharbor/internal/platform/httpapi"
)

// deniedUserIdentity 提供指定认证结果的测试替身，不模拟业务查询或写入。
// 真实 SessionAuthenticator 和 identity.Authenticator 仍执行凭据、会话、用户状态的顺序。
// 空 mode 提供用户 17 的有效身份，供其它注册接口测试核对业务 IO。
type deniedUserIdentity struct {
	mode string // 仅控制指定的签名、会话或用户状态失败；不签发真实 JWT，不连接数据库或 Redis。
}

// Verify 只接受测试 opaque 令牌，返回用户与 SID，或 mode 指定的坏签名／零身份。
func (f deniedUserIdentity) Verify(token string) (string, string, error) {
	if token != "opaque" || f.mode == "signature" {
		return "", "", errors.New("private-auth-source-error")
	}
	if f.mode == "zero-subject" {
		return "0", "sid", nil
	}
	return "17", "sid", nil
}

// Exists 检查 fixture 的 sid，返回过期 false 或读取／取消错误；不修改会话。
func (f deniedUserIdentity) Exists(ctx context.Context, sid string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.mode == "session-error" || sid != "sid" {
		return false, errors.New("private-auth-source-error")
	}
	return f.mode != "expired", nil
}

// Active 检查 fixture 用户 id，返回冻结 false 或读取／取消错误。
func (f deniedUserIdentity) Active(ctx context.Context, id uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.mode == "user-error" || id != 17 {
		return false, errors.New("private-auth-source-error")
	}
	return f.mode != "frozen", nil
}

// TestRegisteredUserAuthenticationFailureWritesOneReply 验证 29 个实际注册的受保护用户端点。
// 各类认证失败只输出一个错误响应，保留 401／403／503、请求号与无敏感原因；业务依赖未装配，不能继续执行。
func TestRegisteredUserAuthenticationFailureWritesOneReply(t *testing.T) {
	endpoints := []struct {
		method string // 公开 HTTP 方法。
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
		mode   string // 认证失败来源；missing 和 malformed 控制请求头。
		status int    // identity 错误对应的 HTTP 状态。
		code   int    // 公开业务错误码。
	}{
		{"missing", 401, 1001}, {"malformed", 401, 1001}, {"signature", 401, 1001},
		{"zero-subject", 401, 1001}, {"expired", 401, 1001}, {"frozen", 403, 1003},
		{"session-error", 503, 5001}, {"user-error", 503, 5001}, {"cancelled", 503, 5001},
	} {
		t.Run(failure.mode, func(t *testing.T) {
			fixture := deniedUserIdentity{mode: failure.mode}
			a := authhttp.SessionAuthenticator{Auth: identity.Authenticator{Tokens: fixture, Sessions: fixture, Users: fixture}}
			r := httpapi.NewRouter()
			userwallet.API{Auth: a}.Register(r)
			userdevice.API{Auth: a}.Register(r)
			usermarketing.API{Auth: a}.Register(r)
			usersettlement.InvoiceAPI{Auth: a}.Register(r)
			userorder.FeedbackAPI{Auth: a}.Register(r)
			userorder.QueryAPI{Auth: a}.Register(r)
			usersettlement.BillAPI{Auth: a}.Register(r)

			usercard.API{Auth: a}.Register(r)
			useridentity.PhoneAPI{Auth: a}.Register(r)
			userpayment.StartAPI{Auth: a}.Register(r)
			userpayment.DevelopmentAPI{Auth: a}.Register(r)
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
