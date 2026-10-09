package repo

import (
	"chargepilot/internal/protocol"
	"github.com/google/uuid"
	"testing"
)

// TestCardEventIdentitySurvivesReplayWithoutCollapsingReswipe 核对刷卡 UUID 重放稳定且再次刷卡不合并；仅验证纯事件键，不访问数据库。
func TestCardEventIdentitySurvivesReplayWithoutCollapsingReswipe(t *testing.T) {
	e := protocol.Event{Type: protocol.CardSwipe, DeviceID: "card-device", Port: 2, CardNumber: 17564161, EventID: uuid.NewString(), RawPayload: []byte{2, 1, 2, 12, 1, 10, 0, 0, 0, 0}}
	first := eventKey(e)
	if first != e.EventID || eventKey(e) != first {
		t.Fatal("retry lost stable event identity")
	}
	e.EventID = uuid.NewString()
	if eventKey(e) == first {
		t.Fatal("separate reswipe collapsed into a raw payload hash")
	}
}
