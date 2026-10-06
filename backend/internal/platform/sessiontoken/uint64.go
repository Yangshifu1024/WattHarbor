package sessiontoken

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// JSONUint64 在JSON中用十进制字符串保存无符号整数，兼容旧整数字面量，不经浮点转换。
// 是否允许零由调用方业务决定；该原语不定义用户、权限版本或会话规则。
type JSONUint64 uint64

// MarshalJSON 返回value的带引号十进制JSON，无IO或错误，避免Lua cjson数字精度损失。
func (value JSONUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(value), 10))
}

// UnmarshalJSON 将raw的十进制字符串或原数字文本赋给value，返回格式／范围错误。
// null、负数、小数、指数和超uint64范围拒绝，不转换float64、不修改存储。
func (value *JSONUint64) UnmarshalJSON(raw []byte) error {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
	}
	if text == "" {
		return fmt.Errorf("empty JSON unsigned integer")
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("invalid JSON unsigned integer")
		}
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return err
	}
	*value = JSONUint64(parsed)
	return nil
}
