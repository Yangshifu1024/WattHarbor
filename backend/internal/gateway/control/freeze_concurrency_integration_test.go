//go:build integration

package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	centralcontract "wattharbor/internal/contracts/central"
	gatewaycontract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/domain/charging"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/testfixture"
)

// TestConcurrentEndFreezeKeepsFirstEvidence 让两个实际注册请求先读到缺失，再以真实唯一键竞争首次冻结。
// 同一身份的不同计量片段只能保留首次写入结果，1062 后重读；高位整数编号冲突仍拒绝，夹具按自身主键清理。
func TestConcurrentEndFreezeKeepsFirstEvidence(t *testing.T) {
	db := gatewayContractDB(t)
	id := testfixture.NewID()
	t.Cleanup(func() {
		if err := db.Table("charge_end_delivery").Where("device_event_id = ?", id).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
			return
		}
		var remaining int64
		if err := db.Table("charge_end_delivery").Where("device_event_id = ?", id).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("freeze fixture remaining=%d err=%v", remaining, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	router := httpapi.NewRouter()
	(DeviceEventAPI{DB: db, ServiceToken: "freeze-concurrency-test"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "freeze-concurrency-test")
	if err != nil {
		t.Fatal(err)
	}
	ready, release := make(chan struct{}, 2), make(chan struct{})
	var gate sync.Once
	defer gate.Do(func() { close(release) })
	var misses, inserts, duplicates atomic.Int32
	queryHook, createHook := "test:freeze-read-"+uuid.NewString(), "test:freeze-create-"+uuid.NewString()
	if err := db.Callback().Query().After("gorm:query").Register(queryHook, func(query *gorm.DB) {
		if query.Statement.Table == "charge_end_delivery" && query.Error == nil && query.RowsAffected == 0 && misses.Add(1) <= 2 {
			ready <- struct{}{}
			select {
			case <-release:
			case <-query.Statement.Context.Done():
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(queryHook); err != nil {
			t.Error(err)
		}
	})
	if err := db.Callback().Create().After("gorm:create").Register(createHook, func(create *gorm.DB) {
		if create.Statement.Table == "charge_end_delivery" {
			inserts.Add(1)
			var collision *mysql.MySQLError
			if errors.As(create.Error, &collision) && collision.Number == 1062 {
				duplicates.Add(1)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Create().Remove(createHook); err != nil {
			t.Error(err)
		}
	})
	// freezeOutcome 收集已退出的客户端请求结果，测试断言仅由主测试协程执行。
	type freezeOutcome struct {
		reply gatewaycontract.FreezeEndDeliveryReply // 首次／重放标志及最终采用的载荷。
		err   error                                  // 传输或业务错误。
	}
	results := make(chan freezeOutcome, 2)
	fact := centralcontract.EndResultRequest{ChargeOrderID: id, OrderNo: "FREEZE-" + uuid.NewString(), PortID: id + 2, Meter: charging.EndMeter{ChargedWh: 1000}}
	for _, energy := range []uint32{1000, 2000} {
		candidate := fact
		candidate.Meter.Segments = []charging.MeterSegment{{EnergyWh: energy}}
		payload, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		request := gatewaycontract.FreezeEndDeliveryRequest{DeviceEventID: id, ChargeOrderID: id, Payload: string(payload)}
		go func() {
			reply, err := client.FreezeEndDelivery(ctx, request)
			results <- freezeOutcome{reply: reply, err: err}
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("both initial reads did not observe the missing row", ctx.Err())
		}
	}
	gate.Do(func() { close(release) })
	var first, replay gatewaycontract.FreezeEndDeliveryReply
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.reply.Frozen {
				replay = result.reply
			} else {
				first = result.reply
			}
		case <-ctx.Done():
			t.Fatal("freeze requests did not exit", ctx.Err())
		}
	}
	if first.Payload == "" || replay.Payload == "" || inserts.Load() != 2 || duplicates.Load() != 1 {
		t.Fatalf("expected one first write and one real 1062 replay: first=%+v replay=%+v inserts=%d duplicates=%d", first, replay, inserts.Load(), duplicates.Load())
	}
	var winner, repeated centralcontract.EndResultRequest
	if json.Unmarshal([]byte(first.Payload), &winner) != nil || json.Unmarshal([]byte(replay.Payload), &repeated) != nil || !reflect.DeepEqual(winner, repeated) || winner.ChargeOrderID != id || winner.PortID != id+2 || len(winner.Meter.Segments) != 1 {
		t.Fatal("replay lost the first complete evidence or exact integer identity")
	}
	fact.Meter.Segments = nil
	payload, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewaycontract.FreezeEndDeliveryRequest{DeviceEventID: id, ChargeOrderID: id, Payload: string(payload)}
	restored, err := client.FreezeEndDelivery(ctx, request)
	if err != nil || !restored.Frozen || restored.Payload != replay.Payload || inserts.Load() != 2 {
		t.Fatalf("existing evidence replay performed another insert or replaced segments: %+v %v", restored, err)
	}
	fact.ChargeOrderID++
	payload, err = json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	request.ChargeOrderID, request.Payload = fact.ChargeOrderID, string(payload)
	_, err = client.FreezeEndDelivery(ctx, request)
	var conflict *serviceclient.HTTPError
	if !errors.As(err, &conflict) || conflict.StatusCode != http.StatusConflict || inserts.Load() != 2 {
		t.Fatalf("changed adjacent high integer identity was not rejected without write: %v", err)
	}
}

// TestEndFreezeKeepsReadAndWriteFailureMessages 验证真实注册端点保留初读取消与数据库拒绝非法 JSON 的不同 503 消息。
// 内部数据库错误不进入响应，无记录写入；失败分类不能把取消或冻结失败包装成成功重放。
func TestEndFreezeKeepsReadAndWriteFailureMessages(t *testing.T) {
	db := gatewayContractDB(t)
	router := httpapi.NewRouter()
	(DeviceEventAPI{DB: db, ServiceToken: "freeze-failure-test"}).Register(router)
	for _, tc := range []struct {
		name     string // 失败阶段。
		payload  string // 待冻结载荷。
		canceled bool   // 是否在读取前取消请求。
		message  string // 公开失败消息。
	}{
		{"initial-read-canceled", `{}`, true, "结束回执暂不可读取"},
		{"invalid-json-write", "not-json", false, "结束回执冻结失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := testfixture.NewID()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			payload, err := json.Marshal(gatewaycontract.FreezeEndDeliveryRequest{DeviceEventID: id, ChargeOrderID: id, Payload: tc.payload})
			if err != nil {
				t.Fatal(err)
			}
			var sqlErr error
			hook := "test:freeze-failure-" + uuid.NewString()
			if err := db.Callback().Create().After("gorm:create").Register(hook, func(create *gorm.DB) {
				if create.Statement.Table == "charge_end_delivery" {
					sqlErr = create.Error
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Create().Remove(hook)
			req := httptest.NewRequest(http.MethodPost, gatewaycontract.FreezeEndDeliveryPath, strings.NewReader(string(payload))).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Service-Token", "freeze-failure-test")
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			var reply httpapi.Envelope
			if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil || out.Code != http.StatusServiceUnavailable || reply.Code != 5003 || reply.Message != tc.message || reply.Data != nil {
				t.Fatalf("failure changed: status=%d reply=%+v err=%v", out.Code, reply, err)
			}
			if tc.canceled && sqlErr != nil {
				t.Fatalf("initial read failure attempted an insert: %v", sqlErr)
			}
			if !tc.canceled {
				var driverErr *mysql.MySQLError
				if !errors.As(sqlErr, &driverErr) || strings.Contains(out.Body.String(), sqlErr.Error()) {
					t.Fatalf("missing real MySQL JSON rejection or exposed its details: %v", sqlErr)
				}
			}
			var remaining int64
			if err := db.Table("charge_end_delivery").Where("device_event_id = ?", id).Count(&remaining).Error; err != nil || remaining != 0 {
				t.Fatalf("failed freeze inserted a row: remaining=%d err=%v", remaining, err)
			}
		})
	}
}
