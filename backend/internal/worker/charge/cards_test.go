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

// TestBalanceQueryAndInvalidPortProduceNativeReplies 使用 t、本地 HTTP 余额回复和真实具名中央客户端，核对原卡号查询及 worker 协议回复：889 分按 0.1 元单位截断为 88、零会话编码保留，255 端口刷卡及零卡号余额查询返回无效回复。失败由 t 报告，关闭本地服务；不访问真实数据库、设备或资金，也不执行冻结、投递和推进流程。
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
