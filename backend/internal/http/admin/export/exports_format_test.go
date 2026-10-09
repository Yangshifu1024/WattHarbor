package export

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chargepilot/internal/domain/operations"
	exportpkg "chargepilot/internal/export"
	"github.com/gin-gonic/gin"
)

// TestExportPeriod 验证导出日期格式、起止顺序及允许的最长日期范围。
func TestExportPeriod(t *testing.T) {
	for _, tc := range []struct {
		// filter 是包含日期边界的导出筛选。
		filter map[string]any
		// ok 表示日期窗口是否应被接受。
		ok bool
	}{
		{map[string]any{"from": "2026-09-01", "to": "2026-09-30"}, true},
		{map[string]any{"from": "2026-09-30", "to": "2026-09-01"}, false},
		{map[string]any{"from": "2026-09-01", "to": "2026-10-02"}, false},
		{map[string]any{"from": "bad", "to": "2026-09-30"}, false},
	} {
		if operations.ValidExportPeriod(tc.filter) != tc.ok {
			t.Fatalf("period validation mismatch: %v", tc.filter)
		}
	}
}

// TestExportRejectsUnsupportedFormats 验证编码器与申请端点拒绝未支持格式，既不输出文件内容也不访问数据库。
func TestExportRejectsUnsupportedFormats(t *testing.T) {
	for _, format := range []string{"pdf", "unknown"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := (exportpkg.FormatWriter{}).Write(context.Background(), &output, format, []string{"账单号"}, [][]string{{"BILL-001"}}); err == nil || output.Len() != 0 {
				t.Fatalf("unsupported format produced output: %v", err)
			}

			body, err := json.Marshal(map[string]any{
				"request_id": "d7e09094-1ca0-4e5e-a52a-f8ef4ab7d2c9",
				"resource":   "bills", "format": format,
				"filter": map[string]any{"from": "2026-09-01", "to": "2026-09-30"},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/exports", bytes.NewReader(body))
			// 实际注册路由先注入鉴权档案；非法格式服务校验仍在数据库 IO 前拒绝。
			c.Set("admin_profile", operations.Profile{})
			ExportTask{}.createTask(c)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "导出格式无效") {
				t.Fatalf("unsupported format was not rejected: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
