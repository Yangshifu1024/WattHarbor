// Package devices 定义与协议无关的充电桩编号规则。
package device

import "regexp"

// 设备编号用于寻址、计费绑定、导入和网关开通，各写入路径共用字符校验。
// 数据库 varchar(64) 只是存储长度；协议特有位数由各适配器检查。
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,32}$`)

// ValidDeviceID 检查编号长度为 8–32 位，并仅包含允许的字符；空串非法。
func ValidDeviceID(deviceID string) bool {
	return deviceIDPattern.MatchString(deviceID)
}
