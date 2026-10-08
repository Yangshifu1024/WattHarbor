package settlement

import (
	"encoding/json"
	"testing"
)

func TestBillJSONPreservesLargeIdentifier(t *testing.T) {
	data, err := json.Marshal(Bill{ID: 8996759476100805168})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"] != "8996759476100805168" {
		t.Fatalf("bill id lost its decimal string contract: %v", payload["id"])
	}
}
