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

// DecimalizeIDField 把一段已序列化的 JSON 对象里指定字段的整数 ID 改写为十进制字符串。
// 用于 DecimalID 覆盖不到的场合：原始 JSON 文本、审计快照、第三方报文被原样存回库后
// 再原样返回。大 ID 以字符串传输，避免 JavaScript number 在发出请求前就丢掉精度。
// 字段缺失或值不是整数时保持原样，不猜测、不改写。
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

// DecimalizeRowIDs 把一行 map 中指定列的 ID 转为十进制字符串。
//
// 用于以 map[string]any 组装的响应（工单列表、审计行等）：这些行没有结构体字段，
// 无法靠 DecimalID 的 MarshalJSON 生效。NULL 保持 null，不变成字符串 "0"——
// 两者含义不同，前端据此判断"未指派"。调用方只应把确实是 ID 的列传进来。
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
