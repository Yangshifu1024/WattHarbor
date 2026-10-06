//go:build integration

// Package testfixture 提供真库测试的私有夹具身份，不参与服务构建或业务编号分配。
package testfixture

import (
	"crypto/rand"
	"encoding/binary"
)

// NewID 返回以随机位生成的大整数夹具主键基址，摆脱跨进程时钟精度和时间主键与自增值的竞争。
// 基址位于 [2^62,2^63-2^14)，上界保留关联偏移空间，且保留大于2^53的精度断言。
// 只生成身份，不查询数据库或改变业务编号；随机身份仍由真实唯一键约束，不宣称数学上绝无碰撞。
func NewID() uint64 {
	var bytes [8]byte
	rand.Read(bytes[:])
	const width = (uint64(1) << 62) - (uint64(1) << 14)
	return binary.BigEndian.Uint64(bytes[:])%width + (uint64(1) << 62)
}
