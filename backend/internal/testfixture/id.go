//go:build integration

// Package testfixture 为真库测试生成独立编号，不参与服务构建或业务编号分配。
package testfixture

import (
	"crypto/rand"
	"encoding/binary"
)

// NewID 随机生成测试主键基址，减少时钟碰撞及与自增主键的冲突。
// 范围为 [2^62,2^63-2^14)，预留关联编号空间，并覆盖大于 2^53 的精度测试。
// 随机编号仍可能碰撞，最终由数据库唯一约束检查。
func NewID() uint64 {
	var bytes [8]byte
	rand.Read(bytes[:])
	const width = (uint64(1) << 62) - (uint64(1) << 14)
	return binary.BigEndian.Uint64(bytes[:])%width + (uint64(1) << 62)
}
