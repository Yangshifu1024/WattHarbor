package admin

import (
	"encoding/json"
	"strings"
)

// DecodeScopeList 解析范围 ID 数组：空白按"未填"处理返回空列表，
// 非法 JSON 交回调用方报错，不静默变成空范围。
func DecodeScopeList(raw string) ([]uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []uint64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}
