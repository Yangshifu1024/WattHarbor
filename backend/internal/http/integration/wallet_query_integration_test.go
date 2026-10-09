//go:build integration

package integration

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	authhttp "chargepilot/internal/http/auth"
	userwallet "chargepilot/internal/http/user/wallet"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/testfixture"
	"chargepilot/internal/wallet"
	"github.com/google/uuid"
)

// TestRegisteredWalletRefundQueryPreservesApprovalAndJSON 使用实际注册路由验证鉴权及公开退款回执。
// 会话及用户状态检查为 fixture，申请和审核为真实 MySQL；外层回滚，未向真实支付渠道请求。
func TestRegisteredWalletRefundQueryPreservesApprovalAndJSON(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	request := uuid.NewString()
	if err := tx.Table("wallet_refund_request").Create(map[string]any{"request_id": request, "user_id": id, "amount_cents": 100, "reason": "验证"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("wallet_risk_review").Create(map[string]any{"request_id": request, "actor_id": 19, "approved": true, "comment": "通过", "response_json": "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("wallet-query-test-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id, 10), Kind: "user", OpenID: "query", SessionID: "session-query", IssuedAt: time.Now().Add(-time.Minute).Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	userwallet.API{WalletQuery: wallet.QueryService{Repo: wallet.UserWalletRepo{DB: tx}}, Auth: authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, walletSession{}, walletUser{})}}.Register(router)
	unauth := httptest.NewRecorder()
	router.ServeHTTP(unauth, httptest.NewRequest("GET", "/api/v1/user/wallet/refunds", nil))
	if unauth.Code != 401 {
		t.Fatalf("anonymous=%d %s", unauth.Code, unauth.Body.String())
	}
	r := httptest.NewRequest("GET", "/api/v1/user/wallet/refunds?page=1&page_size=20", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	var out struct {
		Code int `json:"code"` // 业务响应码
		Data struct {
			UserID string `json:"user_id"` // 大整数编号必须为字符串
			Items  []struct {
				Status       string `json:"status"`        // 审核通过仍为待渠道退款
				RefundOrders []any  `json:"refund_orders"` // 无渠道分笔时为空数组
				Review       struct {
					Approved bool   `json:"approved"` // 审核通过结论，尚不代表渠道已退款
					ActorID  uint64 `json:"actor_id"` // 用户视图中的数值审核人身份
					Comment  string `json:"comment"`  // 审核意见
				} `json:"review"` // 结论，审核人保留数值契约
			} `json:"items"` // 当前页本用户申请
		} `json:"data"` // 响应信封
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out.Code != 0 || out.Data.UserID != strconv.FormatUint(id, 10) || len(out.Data.Items) != 1 {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
	row := out.Data.Items[0]
	if row.Status != "pending" || !row.Review.Approved || row.Review.ActorID != 19 || row.Review.Comment != "通过" || row.RefundOrders == nil {
		t.Fatalf("approval or shape lost: %s", w.Body.String())
	}
}
