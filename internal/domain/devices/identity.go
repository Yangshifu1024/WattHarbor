// Package devices 承载与协议无关的充电桩领域概念。
package devices

import "regexp"

// 设备编号是跨模块共用的键：设备寻址、计费规则挂载、导入清单、网关开通都按它定位。
// 库里的列是 varchar(64)，那只是存储余量；应用层一律按下面的形状校验，
// 四个写入路径共用这一份，不各自实现。
//
// 严格的编号格式由各厂商协议解释，这里校验的是所有协议都要满足的公共下界与字符集。
// 协议特有的位数要求在各自的适配器里判定，不在此处收严。
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,32}$`)

// ValidDeviceID 判断设备编号是否为合法形状。
//
// 少于 8 位或多于 32 位一律拒绝：前者不足以成为全局唯一标识，后者是常见的
// 抄录错误而非真实编号。空串不合法——未归属的设备应显式表达，不能靠空值蒙混。
func ValidDeviceID(deviceID string) bool {
	return deviceIDPattern.MatchString(deviceID)
}
