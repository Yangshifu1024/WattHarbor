// Package devices 承载与协议无关的充电桩领域概念。
package devices

import "regexp"

// deviceIDPattern 是设备对外业务标识的合法形状。
// 设备编号同时用于设备寻址、计费规则挂载和导入清单，是跨模块共用的键，
// 格式判断放在领域层，各写入路径共用同一份，不各自校验。
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidDeviceID 判断设备编号是否为合法形状。空串不合法——未归属的设备应显式表达，
// 不能靠空值蒙混过去。
func ValidDeviceID(deviceID string) bool {
	return deviceIDPattern.MatchString(deviceID)
}
