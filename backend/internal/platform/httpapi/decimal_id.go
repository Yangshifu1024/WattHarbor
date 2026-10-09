package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// DecimalID 接受十进制字符串及兼容旧接口的安全 JSON 整数。
// 大 ID 必须以字符串传输，避免 JavaScript number 在请求发送前丢失精度。
type DecimalID uint64

func (id DecimalID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(id), 10))
}

func (id *DecimalID) UnmarshalJSON(data []byte) error {
	value := string(data)
	quoted := len(data) > 0 && data[0] == '"'
	if quoted {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	}
	if value == "" || len(value) > 20 || len(value) > 1 && value[0] == '0' {
		return errors.New("invalid decimal ID")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return errors.New("invalid decimal ID")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return err
	}
	if !quoted && parsed > 1<<53-1 {
		return errors.New("large IDs must be sent as decimal strings")
	}
	*id = DecimalID(parsed)
	return nil
}

// DecimalizeIDField 将 JSON 对象中指定的整数 ID 转为十进制字符串，避免前端丢失精度。
// 用于原始 JSON 和审计快照；字段缺失或不是整数时保持原样。
func DecimalizeIDField(raw []byte, field string) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	value, ok := object[field]
	if !ok {
		return json.Marshal(object)
	}
	var id uint64
	if err := json.Unmarshal(value, &id); err != nil {
		return json.Marshal(object)
	}
	object[field], _ = json.Marshal(strconv.FormatUint(id, 10))
	return json.Marshal(object)
}

// DecimalizeRowIDs 将 map 中指定的 ID 列转为十进制字符串，NULL 保持 null。
// 只传入确实表示 ID 的列，避免转换金额等其他整数。
func DecimalizeRowIDs(row map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := row[key]; ok && value != nil {
			row[key] = fmt.Sprint(value)
		}
	}
}

// DecimalizeRowIDsAll 批量版本，语义与 DecimalizeRowIDs 相同。
func DecimalizeRowIDsAll(rows []map[string]any, keys ...string) {
	for _, row := range rows {
		DecimalizeRowIDs(row, keys...)
	}
}
