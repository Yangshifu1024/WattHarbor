//go:build contract

package serialization

import (
	"encoding/json"
	"testing"
)

// assertJSON 断言 v 序列化后的**精确字节**等于 want。
//
// 刻意不使用 JSONEq 或任何"语义相等"比较：本目录的目的是锁死字节形状，
// 包括字段顺序、null 与省略的取舍、omitempty 的边界，以及 `,string` 标签。
// 宽松比较会让这些变化全部静默通过。
//
// 失败时同时打印期望与实际，方便直接看出契约差异的位置。
func assertJSON(t *testing.T, v any, want string) {
	t.Helper()
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if string(got) != want {
		t.Errorf("JSON 形状发生变化\n期望：%s\n实际：%s", want, got)
	}
}

// assertEach 逐项跑同一组用例，每个取值一个子测试。
//
// cases 用匿名结构体切片承载「子测试名 + 被序列化的完整结构体 + 期望字节」，
// 由泛型参数 T 统一各域结构体类型；这样每个用例在自己的域文件里排成一张表，
// 一行一项，git diff 能直接看出是哪一项取值或哪一段 JSON 变了。
func assertEach[T any](t *testing.T, cases []struct {
	Name  string
	Value T
	Want  string
}) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			assertJSON(t, c.Value, c.Want)
		})
	}
}

// ptr 返回 v 的指针，用于构造真实结构体里的可选字段。
// 单独包一层是为了让每个用例里的赋值保持一行、可逐行 review。
func ptr[T any](v T) *T { return &v }
