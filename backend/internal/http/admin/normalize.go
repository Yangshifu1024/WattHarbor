package admin

import (
	"encoding/json"
	"strings"
)

// booleanColumns 列出原后台只读投影的布尔列，驱动整数输出按零／非零转换，不新增列或读取数据库。
var booleanColumns = map[string]bool{
	"enabled":                 true,
	"reports_energy":          true,
	"reports_segmented_power": true,
	"user_visible":            true,
}

// NormalizeRows 把驱动返回的原始列整理成可直接序列化的值：
func NormalizeRows(rows []map[string]any) {
	for _, row := range rows {
		for k, v := range row {
			if b, ok := v.([]byte); ok {
				v = string(b)
				row[k] = v
			}
			if k == "event_types" || k == "target_ids" || strings.HasSuffix(k, "_json") {
				if s, ok := v.(string); ok {
					var x any
					if json.Unmarshal([]byte(s), &x) == nil {
						row[k] = x
					}
				}
			}
			if booleanColumns[k] {
				switch n := v.(type) {
				case int64:
					row[k] = n != 0
				case uint64:
					row[k] = n != 0
				case int8:
					row[k] = n != 0
				case bool:
					// 已经是布尔值，原样留着。
				}
			}
		}
	}
}
