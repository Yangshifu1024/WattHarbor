package sessiontoken

import (
	"encoding/json"
	"testing"
)

// TestJSONUint64ExactBoundaries 验证新字符串和旧数字的完整uint64边界，以及错误不能改写已读身份。
func TestJSONUint64ExactBoundaries(t *testing.T) {
	for _, raw := range []string{`18446744073709551615`, `"18446744073709551615"`} {
		var value JSONUint64
		if err := json.Unmarshal([]byte(raw), &value); err != nil || uint64(value) != ^uint64(0) {
			t.Fatalf("exact %s: %d %v", raw, value, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != `"18446744073709551615"` {
			t.Fatalf("encoded %s %v", encoded, err)
		}
	}
	for _, raw := range []string{`null`, `-1`, `1.5`, `1e3`, `18446744073709551616`, `""`, `"1e3"`, `"-1"`, `" 1"`} {
		value := JSONUint64(9007199254740993)
		if err := json.Unmarshal([]byte(raw), &value); err == nil || value != 9007199254740993 {
			t.Fatalf("bad %s changed identity %d err=%v", raw, value, err)
		}
	}
	var zero JSONUint64
	if err := json.Unmarshal([]byte(`0`), &zero); err != nil || zero != 0 {
		t.Fatalf("generic zero %d %v", zero, err)
	}
}
