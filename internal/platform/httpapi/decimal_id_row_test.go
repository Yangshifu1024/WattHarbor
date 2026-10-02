package httpapi

import (
	"reflect"
	"testing"
)

func TestDecimalizeRowIDsKeepsNullAndMissingIntact(t *testing.T) {
	row := map[string]any{
		"id":           uint64(893327963750400123),
		"user_id":      uint64(1),
		"assigned_to":  nil, // 未指派：必须仍是 null，不能变成 "0"
		"status":       "pending",
		"amount_cents": int64(1500), // 非 ID 列未被指定，不应被动过
	}
	DecimalizeRowIDs(row, "id", "user_id", "assigned_to", "absent_column")

	if row["id"] != "893327963750400123" {
		t.Fatalf("大 ID 精度丢失：%#v", row["id"])
	}
	if row["user_id"] != "1" {
		t.Fatalf("user_id = %#v", row["user_id"])
	}
	if v, ok := row["assigned_to"]; !ok || v != nil {
		t.Fatalf("NULL 应保持 null，实得 %#v", v)
	}
	if row["status"] != "pending" || row["amount_cents"] != int64(1500) {
		t.Fatalf("未指定的列被改动了：%#v", row)
	}
}

func TestDecimalizeRowIDsAllAppliesPerRow(t *testing.T) {
	rows := []map[string]any{
		{"id": uint64(10)},
		{"id": uint64(20)},
		{"id": nil},
	}
	DecimalizeRowIDsAll(rows, "id")
	want := []map[string]any{{"id": "10"}, {"id": "20"}, {"id": nil}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %#v", rows)
	}
}
