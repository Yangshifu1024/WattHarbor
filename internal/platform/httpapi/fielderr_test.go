package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBadRequestCarriesFieldErrors(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		BadRequest(c, "参数不合法",
			FieldErr{Field: "amount_cents", Rule: "1..5000000"},
			FieldErr{Field: "request_id", Rule: "uuid"},
		)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	var got struct {
		Code   int        `json:"code"`
		Errors []FieldErr `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	if rec.Code != http.StatusBadRequest || got.Code != CodeBadRequest {
		t.Fatalf("状态码 %d / 业务码 %d", rec.Code, got.Code)
	}
	if len(got.Errors) != 2 {
		t.Fatalf("字段级错误未随响应返回: %+v", got.Errors)
	}
	if got.Errors[0].Field != "amount_cents" || got.Errors[1].Rule != "uuid" {
		t.Fatalf("字段级错误内容不符: %+v", got.Errors)
	}
}

// BadRequest 不带字段错误时，errors 字段必须省略，不得输出空数组。
func TestBadRequestOmitsEmptyErrors(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.GET("/y", func(c *gin.Context) { BadRequest(c, "缺参数") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/y", nil))
	if body := rec.Body.String(); contains(body, `"errors"`) {
		t.Fatalf("无字段错误时不应输出 errors 键: %s", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
