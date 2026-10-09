// 导出共用的日期与单元格检查。
package operations

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxExportPeriod 是导出允许的最大时间跨度。
const MaxExportPeriod = 30 * 24 * time.Hour

// ExportDateLayout 是导出筛选项使用的日期格式。
const ExportDateLayout = "2006-01-02"

// ValidExportPeriod 校验导出日期区间：只接受 from、to 两个键，跨度不超过 MaxExportPeriod。
func ValidExportPeriod(filter map[string]any) bool {
	fromRaw, fromOK := filter["from"].(string)
	toRaw, toOK := filter["to"].(string)
	if !fromOK || !toOK || len(filter) != 2 {
		return false
	}
	from, err1 := time.Parse(ExportDateLayout, fromRaw)
	to, err2 := time.Parse(ExportDateLayout, toRaw)
	return err1 == nil && err2 == nil && !to.Before(from) && to.Sub(from) <= MaxExportPeriod
}

// NextExportDate 返回结束日的下一天，供 issued_at < 日期 的查询包含整个结束日。
// 无法解析为 YYYY-MM-DD 时返回原文本，由调用方拒绝非法参数。
func NextExportDate(raw any) string {
	parsed, err := time.Parse(ExportDateLayout, fmt.Sprint(raw))
	if err != nil {
		return fmt.Sprint(raw)
	}
	return parsed.AddDate(0, 0, 1).Format(ExportDateLayout)
}

// SafeSpreadsheetCell 防止导出文本被表格软件当成公式。
// 以 = + - @ 开头且不能解析为数字的内容加单引号，合法数字保持原样。
func SafeSpreadsheetCell(value string) string {
	if value == "" {
		return value
	}
	if strings.ContainsRune("=+-@", rune(value[0])) {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "'" + value
		}
	}
	return value
}
