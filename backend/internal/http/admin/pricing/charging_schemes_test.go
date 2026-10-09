package pricing

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	charging "chargepilot/internal/domain/charging"
	"github.com/gin-gonic/gin"
)

// TestFullSchemePreviewIsReadOnlyAndRejectsMissingInputs 验证完整方案预览无需数据库，按规则计算退款并拒绝缺失输入。
func TestFullSchemePreviewIsReadOnlyAndRejectsMissingInputs(t *testing.T) {
	r := gin.New()
	r.POST("/preview", API{}.previewChargingScheme)
	start := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
	s := charging.Scheme{Name: "套餐", Packages: []charging.Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 300, Minutes: 120}}}
	for _, tc := range []struct {
		// name 标识本次预览输入场景。
		name string
		// scheme 是请求提交的完整方案，不从数据库补齐。
		scheme charging.Scheme
		// want 是完整或缺失方案对应的 HTTP 状态。
		want int
	}{
		{"valid complete scheme with no DB", s, 200}, {"missing required input", charging.Scheme{}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"scheme": tc.scheme, "package_id": 1, "meter": charging.ActualMeter{StartedAt: start, EndedAt: start.Add(61*time.Minute + 30*time.Second), ChargedSeconds: 3690, ChargedWh: 100}})
			req := httptest.NewRequest("POST", "/preview", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if w.Code == 200 && !bytes.Contains(w.Body.Bytes(), []byte(`"refund_cents":148`)) {
				t.Fatal(w.Body.String())
			}
		})
	}
}
