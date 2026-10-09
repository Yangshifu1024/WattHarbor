package user

import (
	"slices"
)

// AllowedStatus 判断 value 是否为空，或是否出现在以 ASCII 空格分隔的 allowed 列表中；不修改输入。
func AllowedStatus(value, allowed string) bool {
	if value == "" {
		return true
	}
	return slices.Contains(splitFields(allowed), value)
}

// splitFields 仅按 ASCII 空格切分 value，忽略空段并保留其他空白字符，返回非 nil 词集合。
func splitFields(value string) []string {
	out := []string{}
	current := ""
	for _, r := range value {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}
