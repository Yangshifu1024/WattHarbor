//go:build integration

package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	contract "wattharbor/internal/contracts/gateway"
	client "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/protocol"
)

// TestCardDecisionInputKeepsFrozenBypass 验证实际注册输入端点的事件标识校验、已保存回复优先及缺失/取消消息。
// 夹具全部位于外层回滚事务，不执行资金决策或设备回复。
func TestCardDecisionInputKeepsFrozenBypass(t *testing.T) {
	db := gatewayContractDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	board, key := "CARD-INPUT-"+uuid.NewString(), uuid.NewString()
	if err := tx.Table("card_event_delivery").Create(map[string]any{"event_key": key}).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.Event{EventID: key, DeviceID: board, Type: protocol.CardBalanceQuery, ReceivedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("device_event").Create(map[string]any{"event_key": key, "protocol_name": "dc589", "device_id": board, "event_type": "card_balance_query", "event_json": string(raw), "received_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"event_key": key})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/internal/card-events/decide-begin"
	// 在实际锁读的事务连接写入私有 attempts 作为生命周期观测：普通成功提交，身份错误和已有回复的返回分支回滚。
	// 正式 begin 不更新 attempts；此写入仅用于观察事务是否提交。
	hook := "test:card-decision-rollback-" + uuid.NewString()
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(query *gorm.DB) {
		if query.Statement.Table == "card_event_delivery" && query.Error == nil && query.RowsAffected == 1 && query.Statement.Clauses["FOR"].Name != "" {
			query.AddError(query.Session(&gorm.Session{NewDB: true}).Table("card_event_delivery").Where("event_key=?", key).Update("attempts", gorm.Expr("attempts+1")).Error)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(hook)
	status, reply := callFaultEndpoint(t, tx, http.MethodPost, path, string(body), context.Background())
	var input struct {
		EventJSON    string `json:"event_json"`    // 协议事件全文。
		ResponseJSON string `json:"response_json"` // 已保存回复，未冻结时为空。
	}
	var event protocol.Event
	if err := json.Unmarshal(reply.Data, &input); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(input.EventJSON), &event); err != nil || status != 200 || reply.Code != 0 || event.EventID != key || input.ResponseJSON != "" {
		t.Fatalf("original input: status=%d input=%+v err=%v", status, input, err)
	}
	var attempts uint32
	if err := tx.Table("card_event_delivery").Select("attempts").Where("event_key=?", key).Take(&attempts).Error; err != nil || attempts != 1 {
		t.Fatalf("normal input did not commit: attempts=%d err=%v", attempts, err)
	}
	// 合法 JSON 但错事件标识必须 409；已有保存值则原样绕过该错误事件。
	if err := tx.Table("device_event").Where("event_key=?", key).Update("event_json", `{"EventID":"wrong"}`).Error; err != nil {
		t.Fatal(err)
	}
	status, reply = callFaultEndpoint(t, tx, http.MethodPost, path, string(body), context.Background())
	if status != 409 || reply.Code != 2009 || reply.Message != "card event identity mismatch" || string(reply.Data) != "null" {
		t.Fatalf("identity conflict: status=%d reply=%+v", status, reply)
	}
	if err := tx.Table("card_event_delivery").Select("attempts").Where("event_key=?", key).Take(&attempts).Error; err != nil || attempts != 1 {
		t.Fatalf("identity failure did not roll back: attempts=%d err=%v", attempts, err)
	}
	if err := tx.Table("card_event_delivery").Where("event_key=?", key).Update("response_json", `{"original":true}`).Error; err != nil {
		t.Fatal(err)
	}
	status, reply = callFaultEndpoint(t, tx, http.MethodPost, path, string(body), context.Background())
	input.EventJSON, input.ResponseJSON = "", ""
	if err := json.Unmarshal(reply.Data, &input); err != nil || status != 200 || reply.Code != 0 || input.EventJSON != "" || input.ResponseJSON == "" {
		t.Fatalf("frozen bypass: status=%d input=%+v err=%v", status, input, err)
	}
	var frozen map[string]bool
	if err := json.Unmarshal([]byte(input.ResponseJSON), &frozen); err != nil || !frozen["original"] {
		t.Fatalf("frozen value changed: %q %v", input.ResponseJSON, err)
	}
	if err := tx.Table("card_event_delivery").Select("attempts").Where("event_key=?", key).Take(&attempts).Error; err != nil || attempts != 1 {
		t.Fatalf("frozen normal exit did not roll back: attempts=%d err=%v", attempts, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, reply = callFaultEndpoint(t, tx, http.MethodPost, path, string(body), ctx)
	if status != 503 || reply.Code != 5003 || reply.Message != "刷卡决策输入暂不可读取" || string(reply.Data) != "null" {
		t.Fatalf("canceled input: status=%d reply=%+v", status, reply)
	}
	missing, _ := json.Marshal(map[string]string{"event_key": uuid.NewString()})
	status, reply = callFaultEndpoint(t, tx, http.MethodPost, path, string(missing), context.Background())
	if status != 503 || reply.Code != 5003 || reply.Message != "刷卡决策输入暂不可读取" {
		t.Fatalf("missing input: status=%d reply=%+v", status, reply)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, table := range []string{"device_event", "card_event_delivery"} {
		if err := db.Table(table).Where("event_key=?", key).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("fixture remaining %s=%d err=%v", table, n, err)
		}
	}
}

// TestCardFreezeKeepsStageFailureMessages 验证取消、真实 JSON 列拒绝和缺投递行保持 UPDATE／后续读取阶段错误，失败不写冻结值。
// 只在回滚事务中造随机空冻结夹具，不触发中央或设备投递。
func TestCardFreezeKeepsStageFailureMessages(t *testing.T) {
	db := gatewayContractDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	key := uuid.NewString()
	if err := tx.Table("card_event_delivery").Create(map[string]any{"event_key": key}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range []struct {
		ctx     context.Context // 请求取消范围。
		key     string          // 存在或缺失的投递标识。
		payload string          // 候选 JSON，非法文本交真实列拒绝。
		message string          // 阶段消息。
	}{
		{ctx, key, `{}`, "刷卡回复冻结暂不可用"},
		{context.Background(), key, `not-json`, "刷卡回复冻结暂不可用"},
		{context.Background(), uuid.NewString(), `{}`, "刷卡回复暂不可读取"},
	} {
		body, err := json.Marshal(contract.CardFreezeRequest{EventKey: check.key, ResponseJSON: check.payload})
		if err != nil {
			t.Fatal(err)
		}
		status, reply := callFaultEndpoint(t, tx, http.MethodPost, contract.CardFreezePath, string(body), check.ctx)
		if status != 503 || reply.Code != 5003 || reply.Message != check.message || string(reply.Data) != "null" {
			t.Fatalf("freeze stage: status=%d reply=%+v want=%s", status, reply, check.message)
		}
	}
	var n int64
	if err := tx.Table("card_event_delivery").Where("event_key=? AND response_json IS NULL", key).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("failure wrote frozen value: n=%d err=%v", n, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("card_event_delivery").Where("event_key=?", key).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("fixture remaining=%d err=%v", n, err)
	}
}

// TestConcurrentCardFreezeKeepsFirstReply 验证实际注册专用客户端在真实条件 UPDATE 竞争时恰一次首次写入，重放返回同一完整回复。
// 仅随机卡投递夹具，请求退出后按自身主键清理零残留，不涉及钱包或 TCP。
func TestConcurrentCardFreezeKeepsFirstReply(t *testing.T) {
	db := gatewayContractDB(t)
	key := uuid.NewString()
	t.Cleanup(func() {
		if err := db.Table("card_event_delivery").Where("event_key=?", key).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
			return
		}
		var n int64
		if err := db.Table("card_event_delivery").Where("event_key=?", key).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("fixture remaining=%d err=%v", n, err)
		}
	})
	if err := db.Table("card_event_delivery").Create(map[string]any{"event_key": key}).Error; err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	(DeviceEventAPI{DB: db, ServiceToken: "card-freeze-test"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	edge, err := client.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "card-freeze-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready, release := make(chan struct{}, 2), make(chan struct{})
	var gate sync.Once
	defer gate.Do(func() { close(release) })
	var attempts atomic.Int32
	hook := "test:card-freeze-gate-" + uuid.NewString()
	if err := db.Callback().Update().Before("gorm:update").Register(hook, func(update *gorm.DB) {
		if update.Statement.Table == "card_event_delivery" && attempts.Add(1) <= 2 {
			ready <- struct{}{}
			select {
			case <-release:
			case <-update.Statement.Context.Done():
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Update().Remove(hook); err != nil {
			t.Error(err)
		}
	})
	// outcome 只收已退出的客户端请求，测试断言由主测试协程执行。
	type outcome struct {
		reply contract.CardFreezeReply // 首次写入结果及完整最终回复。
		err   error                    // 传输或契约错误。
	}
	results := make(chan outcome, 2)
	for _, units := range []uint16{101, 202} {
		raw, err := json.Marshal(contract.CardReplyRequest{Accepted: true, DeviceID: "frozen-original", Kind: protocol.CommandCardBalance, CardNumber: 42, BalanceUnits: units})
		if err != nil {
			t.Fatal(err)
		}
		request := contract.CardFreezeRequest{EventKey: key, ResponseJSON: string(raw)}
		go func() { reply, err := edge.FreezeCardResponse(ctx, request); results <- outcome{reply, err} }()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("both conditional updates did not arrive", ctx.Err())
		}
	}
	gate.Do(func() { close(release) })
	var first, replay contract.CardFreezeReply
	firstCount := 0
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.reply.Stored {
				first = result.reply
				firstCount++
			} else {
				replay = result.reply
			}
		case <-ctx.Done():
			t.Fatal("card freeze requests did not exit", ctx.Err())
		}
	}
	var original, repeated contract.CardReplyRequest
	if json.Unmarshal([]byte(first.ResponseJSON), &original) != nil || json.Unmarshal([]byte(replay.ResponseJSON), &repeated) != nil || firstCount != 1 || !reflect.DeepEqual(original, repeated) || (original.BalanceUnits != 101 && original.BalanceUnits != 202) || attempts.Load() != 2 {
		t.Fatalf("first reply lost: first=%+v replay=%+v firstCount=%d attempts=%d", first, replay, firstCount, attempts.Load())
	}
	restored, err := edge.FreezeCardResponse(ctx, contract.CardFreezeRequest{EventKey: key, ResponseJSON: `{"replacement":true}`})
	if err != nil || restored.Stored || restored.ResponseJSON != replay.ResponseJSON {
		t.Fatalf("replay overwrite: %+v err=%v", restored, err)
	}
	var stored string
	if err := db.Table("card_event_delivery").Select("response_json").Where("event_key=?", key).Take(&stored).Error; err != nil || stored != replay.ResponseJSON {
		t.Fatalf("database winner=%q err=%v", stored, err)
	}
}
