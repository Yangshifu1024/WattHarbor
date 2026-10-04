package export

import (
	"bytes"
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExportFormats(t *testing.T) {
	headers := []string{"账单号", "电费(分)", "服务费(分)", "合计(分)", "退款(分)"}
	lines := [][]string{{"BILL-001", "200", "50", "250", "0"}, {"=1+1", "300", "75", "375", "25"}}
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeExport(&output, format, headers, lines); err != nil {
				t.Fatal(err)
			}
			switch format {
			case "csv":
				if !strings.Contains(output.String(), "'=1+1") {
					t.Fatalf("CSV formula was not escaped: %s", output.String())
				}
			case "xlsx":
				book, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				defer book.Close()
				value, err := book.GetCellValue("Sheet1", "A3")
				if err != nil || value != "'=1+1" {
					t.Fatalf("XLSX formula was not escaped: %q %v", value, err)
				}
			}
		})
	}
}

func TestExportPeriod(t *testing.T) {
	for _, tc := range []struct {
		filter map[string]any
		ok     bool
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

func TestExportRejectsUnsupportedFormats(t *testing.T) {
	for _, format := range []string{"pdf", "unknown"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeExport(&output, format, []string{"账单号"}, [][]string{{"BILL-001"}}); err == nil || output.Len() != 0 {
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
			ExportTask{}.createTask(c)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "导出格式无效") {
				t.Fatalf("unsupported format was not rejected: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
