package database

import (
	"bytes"
	"testing"
)

// 监管报送的幂等靠"内容一致"判定。MySQL 的 JSON 列会重排键、丢掉多余空白，
// 所以库里读回来的字节和客户端发来的不一定逐字节相同——必须先归一化再比。
// 忘了这一步，重投同一笔报送就会被判成冲突。
func TestCanonicalJSONEqualizesKeyOrderAndWhitespace(t *testing.T) {
	fromClient := []byte(`{"event_id":"E1","object_type":"order","object_key":"O1","payload":{"a":1,"b":2}}`)
	fromMySQL := []byte(`{ "object_key": "O1", "payload": { "b": 2, "a": 1 }, "event_id": "E1",
	  "object_type": "order" }`)
	a, err := canonicalJSON(fromClient)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON(fromMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("键序或空白不同的等价 JSON 应判定为一致:\n a=%s\n b=%s", a, b)
	}
}

// 归一化不能把不同的内容判成一致——否则改一个字节就能顶替掉别人入队的报送。
func TestCanonicalJSONStillDistinguishesDifferentContent(t *testing.T) {
	for name, pair := range map[string][2]string{
		"值不同":   {`{"object_key":"O1"}`, `{"object_key":"O2"}`},
		"键不同":   {`{"object_key":"O1"}`, `{"key":"O1"}`},
		"少一个字段": {`{"a":1,"b":2}`, `{"a":1}`},
		"多一个字段": {`{"a":1}`, `{"a":1,"b":2}`},
		"类型不同":  {`{"n":1}`, `{"n":"1"}`},
		"数组顺序":  {`{"xs":[1,2]}`, `{"xs":[2,1]}`},
	} {
		a, err := canonicalJSON([]byte(pair[0]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, err := canonicalJSON([]byte(pair[1]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Equal(a, b) {
			t.Errorf("%s: 内容不同却被判为一致 (%s vs %s)", name, a, b)
		}
	}
}

// 损坏的 JSON 必须报错，不能归一化成"空内容"后蒙混过关。
func TestCanonicalJSONRejectsCorruptInput(t *testing.T) {
	for _, raw := range []string{"", "{", `{"a":}`, "not json"} {
		if _, err := canonicalJSON([]byte(raw)); err == nil {
			t.Errorf("%q 应报解析错误而不是静默通过", raw)
		}
	}
}

// 嵌套对象与数组也要归一化：MySQL 一样会重排内层的键。
func TestCanonicalJSONNormalizesNestedStructures(t *testing.T) {
	a, err := canonicalJSON([]byte(`{"outer":{"x":[1,{"p":1,"q":2}],"y":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON([]byte(`{"outer":{"y":true,"x":[1,{"q":2,"p":1}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("嵌套结构未归一化: %s vs %s", a, b)
	}
}
