package sessiontoken

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// JSONUint64 用十进制字符串保存无符号整数，也接受旧版 JSON 整数，避免浮点转换丢失精度。
// 是否允许零由调用方业务决定；此类型不检查用户、权限或会话规则。
type JSONUint64 uint64

// MarshalJSON 将整数编码为十进制字符串，避免 Lua cjson 丢失精度。
func (value JSONUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(value), 10))
}

// UnmarshalJSON 接受十进制字符串或整数字面量；拒绝 null、负数、小数、指数和超出 uint64 范围的值。
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
