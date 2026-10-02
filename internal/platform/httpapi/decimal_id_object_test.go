package httpapi

import (
	"encoding/json"
	"testing"
)

func TestDecimalizeIDFieldKeepsPersistedNumbersExact(t *testing.T) {
	raw := json.RawMessage(`{"user_id":893327963750400123,"charge_order_id":7,"meter":{"charged_wh":12}}`)
	out, err := DecimalizeIDField(raw, "user_id")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	// 目标字段改成十进制字符串；其余字段一律不动，包括嵌套对象。
	if string(fields["user_id"]) != `"893327963750400123"` || string(fields["charge_order_id"]) != "7" {
		t.Fatal(string(out))
	}
	if string(fields["meter"]) != `{"charged_wh":12}` {
		t.Fatal(string(out))
	}
	// 入参不得被就地改写。
	if string(raw) != `{"user_id":893327963750400123,"charge_order_id":7,"meter":{"charged_wh":12}}` {
		t.Fatal("persisted input changed")
	}
}

func TestDecimalizeIDFieldLeavesMissingOrNonIntegerField(t *testing.T) {
	for _, tc := range []struct{ raw, field, want string }{
		{`{"user_id":7}`, "missing_field", `{"user_id":7}`},
		{`{"user_id":"already"}`, "user_id", `{"user_id":"already"}`},
		{`null`, "user_id", `null`},
	} {
		out, err := DecimalizeIDField(json.RawMessage(tc.raw), tc.field)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != tc.want {
			t.Fatalf("got %s want %s", out, tc.want)
		}
	}
}
