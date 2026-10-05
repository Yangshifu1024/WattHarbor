package export

import (
	"bytes"
	"context"
	"github.com/xuri/excelize/v2"
	"strings"
	"testing"
)

func TestExportFormats(t *testing.T) {
	headers := []string{"账单号", "电费(分)", "服务费(分)", "合计(分)", "退款(分)"}
	lines := [][]string{{"BILL-001", "200", "50", "250", "0"}, {"=1+1", "300", "75", "375", "25"}}
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := (FormatWriter{}).Write(context.Background(), &output, format, headers, lines); err != nil {
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
