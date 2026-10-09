package charge

import (
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBalanceQueryAndInvalidPortProduceNativeReplies 用本地 HTTP 服务核对卡号查询、协议单位换算和无效端口回复。
func TestBalanceQueryAndInvalidPortProduceNativeReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("card_no") != "17564161" {
			t.Error(r.URL)
		}
		w.Write([]byte(`{"code":0,"data":{"valid":true,"balance_cents":889}}`))
	}))
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{}, server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	d := CardDispatcher{Central: client}
	r, err := d.decide(context.Background(), protocol.Event{Type: protocol.CardBalanceQuery, DeviceID: "device", CardNumber: 17564161})
	if err != nil || r.Kind != protocol.CommandCardBalance || r.Invalid || r.BalanceUnits != 88 || r.Session != "000000000000" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = d.decide(context.Background(), protocol.Event{Type: protocol.CardSwipe, Port: 255, CardNumber: 17564161})
	if err != nil || r.Kind != protocol.CommandCardDenied || !r.Invalid {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = d.decide(context.Background(), protocol.Event{Type: protocol.CardBalanceQuery, CardNumber: 0})
	if err != nil || !r.Invalid || r.BalanceUnits != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
