package device

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// DeviceInput 要求完整提交资料字段；RawMessage 区分缺失字段和显式 null，避免意外清空。
type DeviceInput struct {
	Model             json.RawMessage `json:"model"`               // 必填字段，可为 null 或最长 128 字符的型号
	SerialNo          json.RawMessage `json:"serial_no"`           // 必填字段，可为 null 或最长 128 字符的序列号
	WarrantyUntil     json.RawMessage `json:"warranty_until"`      // 必填字段，可空或带时区的有效日期
	Tags              json.RawMessage `json:"tags"`                // 必填数组，可为空，最多二十项且无空项或重复
	ExpectedUpdatedAt json.RawMessage `json:"expected_updated_at"` // 必填资料更新时间，参与行锁内版本核对
}

// Metadata 解析完整资料和 UTC 毫秒版本，缺字段或不合法时返回输入错误。
func (in DeviceInput) Metadata() (Device, time.Time, error) {
	var row Device
	var expected time.Time
	for _, value := range []json.RawMessage{in.Model, in.SerialNo, in.WarrantyUntil, in.Tags, in.ExpectedUpdatedAt} {
		if len(value) == 0 {
			return row, expected, fmt.Errorf("请完整提交设备资料和原更新时间")
		}
	}
	text := func(raw json.RawMessage) (*string, error) {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("型号和序列号必须是文本或 null")
		}
		if value == nil {
			return nil, nil
		}
		trimmed := strings.TrimSpace(*value)
		if utf8.RuneCountInString(trimmed) > 128 {
			return nil, fmt.Errorf("型号和序列号不能超过 128 个字符")
		}
		if trimmed == "" {
			return nil, nil
		}
		return &trimmed, nil
	}
	date := func(raw json.RawMessage) (*time.Time, error) {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("日期必须是带时区的 ISO 8601 时间或 null")
		}
		if value == nil {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return nil, fmt.Errorf("日期必须是带时区的 ISO 8601 时间或 null")
		}
		parsed = parsed.UTC().Truncate(time.Millisecond)
		if parsed.Year() < 1000 || parsed.Year() > 9999 {
			return nil, fmt.Errorf("日期年份必须在 1000 至 9999 之间")
		}
		return &parsed, nil
	}
	var err error
	if row.Model, err = text(in.Model); err != nil {
		return row, expected, err
	}
	if row.SerialNo, err = text(in.SerialNo); err != nil {
		return row, expected, err
	}
	if row.WarrantyUntil, err = date(in.WarrantyUntil); err != nil {
		return row, expected, err
	}
	if err := json.Unmarshal(in.Tags, &row.Tags); err != nil || row.Tags == nil || len(row.Tags) > 20 {
		return row, expected, fmt.Errorf("标签必须是数组，最多 20 项")
	}
	seen := map[string]bool{}
	for i, tag := range row.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || utf8.RuneCountInString(tag) > 32 || seen[tag] {
			return row, expected, fmt.Errorf("标签不能为空、重复或超过 32 个字符")
		}
		seen[tag] = true
		row.Tags[i] = tag
	}
	expectedAt, err := date(in.ExpectedUpdatedAt)
	if err != nil || expectedAt == nil {
		return row, expected, fmt.Errorf("请提供设备原更新时间 expected_updated_at")
	}
	return row, *expectedAt, nil
}

// NextDeviceUpdateTime 取当前时间与旧版本加一毫秒的较大值，保证版本递增。
func NextDeviceUpdateTime(previous time.Time) time.Time {
	next := time.Now().UTC().Truncate(time.Millisecond)
	if !next.After(previous) {
		next = previous.Add(time.Millisecond)
	}
	return next
}
