//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"chargepilot/internal/charging/finish"
	"chargepilot/internal/charging/start"
	centralcontract "chargepilot/internal/contracts/central"
	rules "chargepilot/internal/domain/charging"
	centralhttp "chargepilot/internal/http/central"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// lifecycleAtomicFixture 保存已提交的测试订单，以及独立的写入和观察连接。
type lifecycleAtomicFixture struct {
	Writer       *gorm.DB  // 真正 Apply 使用的中央池，事务由生产用例开启并提交。
	Observer     *gorm.DB  // 不共享 Writer 的物理连接，观察写入是否提交或回滚。
	ID           uint64    // 私有大订单及支付身份，所有偏移均在 NewID 预留范围内。
	PortID       uint64    // 私有中央端口主键，成功启动写入及结束释放必须匹配它。
	OtherOwner   uint64    // 同私有端口上的另一订单编号，用于检查旧结束请求不能释放新占用。
	DeviceID     string    // 私有设备编号，避免唯一设备端口与其他测试相撞。
	OrderNo      string    // 业务订单号，重复调用不改变。
	StartCommand string    // 启动 UUID，也是启动事件及 Outbox 身份。
	StopCommand  string    // 结束 UUID，也是结束事件及 Outbox 身份。
	StartedAt    time.Time // UTC 毫秒启动时刻。
	EndedAt      time.Time // UTC 毫秒结束时刻，晚于启动一小时。
	Month        string    // 订单及支付复合主键的 UTC 月首，清理不得省略。
}

// newLifecycleAtomicFixture 创建 paid 或 charging 测试订单及启动凭据，使用独立连接观察提交。
// 不调用支付或退款渠道，清理后关闭连接池。
func newLifecycleAtomicFixture(t *testing.T, charging bool) *lifecycleAtomicFixture {
	t.Helper()
	id := testfixture.NewID()
	ended := time.Now().UTC().Truncate(time.Millisecond)
	f := &lifecycleAtomicFixture{Writer: cardRouteDB(t, "TEST_MYSQL_URL"), Observer: cardRouteDB(t, "TEST_MYSQL_URL"), ID: id, PortID: id + 1, OtherOwner: id + 2,
		DeviceID: "LA" + uuid.NewString(), OrderNo: "C-LA-" + uuid.NewString(), StartCommand: uuid.NewString(), StopCommand: uuid.NewString(), StartedAt: ended.Add(-time.Hour), EndedAt: ended, Month: ended.Format("2006-01") + "-01"}
	var physical [2]uint64
	for index, db := range []*gorm.DB{f.Writer, f.Observer} {
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		if err := db.Raw("SELECT CONNECTION_ID()").Scan(&physical[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if physical[0] == physical[1] {
		t.Fatal("lifecycle writer and observer must use different physical connections")
	}
	t.Cleanup(func() { f.cleanup(t) })
	if err := f.Writer.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("payment_order").Create(map[string]any{"id": id, "order_no": "P-" + f.OrderNo, "biz_type": "charge", "biz_id": id, "user_id": id, "pay_method": "wechat", "total_cents": 100, "paid_cents": 100, "status": "paid", "created_month": f.Month}).Error; err != nil {
			return err
		}
		row := map[string]any{"id": id, "order_no": f.OrderNo, "user_id": id, "payment_order_id": id, "device_id": f.DeviceID, "port_no": 1, "status": "paid", "payment_status": "paid", "created_month": f.Month}
		if charging {
			row["status"], row["started_at"] = "charging", f.StartedAt
		}
		if err := tx.Table("charge_order").Create(row).Error; err != nil {
			return err
		}
		if !charging {
			return nil
		}
		if err := tx.Table("charge_start_receipt").Create(map[string]any{"charge_order_id": id, "command_id": f.StartCommand, "success": true, "port_id": f.PortID}).Error; err != nil {
			return err
		}
		return tx.Table("active_port_charge").Create(map[string]any{"port_id": f.PortID, "device_id": f.DeviceID, "port_no": 1, "charge_order_id": id, "user_id": id, "started_at": f.StartedAt}).Error
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// startRequest 构造该订单的成功启动请求，复用原命令编号和时刻。
func (f *lifecycleAtomicFixture) startRequest() centralcontract.StartResultRequest {
	return centralcontract.StartResultRequest{CommandID: f.StartCommand, ChargeOrderID: f.ID, OrderNo: f.OrderNo, DeviceID: f.DeviceID, PortNo: 1, PortID: f.PortID, Success: true, ResultCode: 0, OccurredAt: f.StartedAt}
}

// endResult 构造该订单的结束请求，电量为 1200 Wh，时长为 3600 秒。
func (f *lifecycleAtomicFixture) endResult() finish.EndResult {
	return finish.EndResult{OrderNo: f.OrderNo, ChargeOrderID: f.ID, StartCommandID: f.StartCommand, StopCommandID: f.StopCommand, DeviceID: f.DeviceID, PortNo: 1, PortID: f.PortID,
		Meter: rules.EndMeter{ChargedWh: 1200, ChargedSeconds: 3600, EndedAt: f.EndedAt, StopReason: 7}}
}

// assertCount 通过独立 Observer 核对私有匹配记录数；读错或数量不符终止测试。
func (f *lifecycleAtomicFixture) assertCount(t *testing.T, table, where string, want int64, args ...any) {
	t.Helper()
	var count int64
	if err := f.Observer.Table(table).Where(where, args...).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%s count=%d want=%d: %v", table, count, want, err)
	}
}

// assertState 核对阶段全部可见记录及状态；只有实际完成的 Apply 才能留下对应事件、Outbox 或结束任务。
// Observer 不复用用例事务，启动时核对真实回执的可空字段、占用与事件载荷，结束时核对计量及启动凭据不变。
func (f *lifecycleAtomicFixture) assertState(t *testing.T, charging, applied bool) {
	t.Helper()
	var row struct {
		Status         string         `gorm:"column:status"`          // 持久生命周期状态，必须与本次提交阶段一致。
		StartedAt      sql.NullTime   `gorm:"column:started_at"`      // 可空启动时刻，启动 rollback 时必须仍 NULL。
		EndedAt        sql.NullTime   `gorm:"column:ended_at"`        // 可空结束时刻，结束 rollback 时必须仍 NULL。
		ChargedKWh     sql.NullString `gorm:"column:charged_kwh"`     // DECIMAL 电量文本，显式映射 kwh 缩写，不通过浮点比较丢失精度。
		ChargedSeconds sql.NullInt64  `gorm:"column:charged_seconds"` // 结束秒数，未结束时必须 NULL。
	}
	if err := f.Observer.Table("charge_order").Where("id=? AND created_month=?", f.ID, f.Month).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	wantStatus := "paid"
	if charging || applied {
		wantStatus = "charging"
	}
	if charging && applied {
		wantStatus = "completed"
	}
	if row.Status != wantStatus || row.StartedAt.Valid != (charging || applied) || row.EndedAt.Valid != (charging && applied) {
		t.Fatalf("lifecycle state=%+v want=%s charging=%v applied=%v", row, wantStatus, charging, applied)
	}
	if row.StartedAt.Valid && !row.StartedAt.Time.Equal(f.StartedAt) {
		t.Fatalf("original started_at changed: %+v", row)
	}
	if charging && applied {
		if !row.EndedAt.Time.Equal(f.EndedAt) || !row.ChargedKWh.Valid || row.ChargedKWh.String != "1.2000" || !row.ChargedSeconds.Valid || row.ChargedSeconds.Int64 != 3600 {
			t.Fatalf("original end meter=%+v", row)
		}
	} else if row.ChargedKWh.Valid || row.ChargedSeconds.Valid {
		t.Fatalf("unconfirmed end meter persisted: %+v", row)
	}
	var changed int64
	if applied {
		changed = 1
	}
	startCount, portCount := changed, changed
	if charging {
		startCount = 1
		portCount = 1 - changed
	}
	f.assertCount(t, "charge_start_receipt", "charge_order_id=?", startCount, f.ID)
	f.assertCount(t, "active_port_charge", "port_id=? AND device_id=? AND charge_order_id=?", portCount, f.PortID, f.DeviceID, f.ID)
	endCount, jobCount := int64(0), int64(0)
	command, stream, event := f.StartCommand, "charge_started_stream", "start_acked"
	if charging {
		endCount, jobCount = changed, changed
		command, stream, event = f.StopCommand, "charge_ended_stream", "charge_ended"
	}
	f.assertCount(t, "charge_end_receipt", "charge_order_id=?", endCount, f.ID)
	f.assertCount(t, "charge_billing_job", "charge_order_id=?", jobCount, f.ID)
	f.assertCount(t, "charge_event_log", "charge_order_id=?", changed, f.ID)
	f.assertCount(t, "event_outbox", "event_id IN ?", changed, []string{f.StartCommand, f.StopCommand})
	if !applied {
		return
	}
	f.assertCount(t, "charge_event_log", "charge_order_id=? AND event_id=? AND event=? AND actor='gateway'", 1, f.ID, command, event)
	var outbox struct {
		Stream       string `gorm:"column:stream"`        // 目标流标签，不随 repo 抽取改变。
		EnvelopeJSON string `gorm:"column:envelope_json"` // 最终回执 JSON，核对请求字段和值未改变。
	}
	if err := f.Observer.Table("event_outbox").Where("event_id=?", command).Take(&outbox).Error; err != nil || outbox.Stream != stream {
		t.Fatalf("original outbox=%+v: %v", outbox, err)
	}
	var want []byte
	var err error
	if charging {
		want, err = json.Marshal(f.endResult())
	} else {
		want, err = json.Marshal(f.startRequest())
	}
	if err != nil || !sameCardJSON(t, outbox.EnvelopeJSON, string(want)) {
		t.Fatalf("original outbox payload changed: %s %s: %v", outbox.EnvelopeJSON, want, err)
	}
	if !charging {
		f.assertCount(t, "charge_start_receipt", "charge_order_id=? AND command_id=? AND order_no=? AND device_id=? AND port_no=1 AND port_id=? AND success=1 AND result_code=0 AND occurred_at=?", 1, f.ID, f.StartCommand, f.OrderNo, f.DeviceID, f.PortID, f.StartedAt)
		f.assertCount(t, "active_port_charge", "port_id=? AND device_id=? AND port_no=1 AND charge_order_id=? AND user_id=? AND started_at=?", 1, f.PortID, f.DeviceID, f.ID, f.ID, f.StartedAt)
	}
}

// cleanup 按私有主键清理提交夹具并逐表验证零残留；自增事件先普通枚举主键，Outbox 复用隔离清理能力。
// 清理不使用被取消的业务 context，不借外层 rollback 掩盖生产提交，也不删除其他订单的记录。
func (f *lifecycleAtomicFixture) cleanup(t *testing.T) {
	t.Helper()
	var ids []uint64
	if err := f.Observer.Table("charge_event_log").Where("charge_order_id=?", f.ID).Order("id").Pluck("id", &ids).Error; err != nil {
		t.Errorf("enumerate own events: %v", err)
	}
	for _, id := range ids {
		if err := f.Observer.Table("charge_event_log").Where("id=? AND charge_order_id=?", id, f.ID).Delete(map[string]any{}).Error; err != nil {
			t.Errorf("cleanup event: %v", err)
		}
	}
	if err := testfixture.DeleteOutbox(f.Observer, "event_id IN ?", []string{f.StartCommand, f.StopCommand}); err != nil {
		t.Errorf("cleanup own outbox: %v", err)
	}
	for _, item := range []struct {
		table string // 仅本夹具创建或用例产生的目标表。
		where string // 全部主键字段及必要归属条件，保持参数化。
		args  []any  // 私有身份／分区日期，不推导其他测试的身份。
	}{
		{"charge_billing_job", "charge_order_id=?", []any{f.ID}},
		{"charge_end_receipt", "charge_order_id=?", []any{f.ID}},
		{"charge_start_receipt", "charge_order_id=?", []any{f.ID}},
		{"active_port_charge", "port_id=? AND device_id=?", []any{f.PortID, f.DeviceID}},
		{"charge_order", "id=? AND created_month=?", []any{f.ID, f.Month}},
		{"charge_order", "id=? AND created_month=?", []any{f.OtherOwner, f.Month}},
		{"payment_order", "id=? AND created_month=?", []any{f.ID, f.Month}},
	} {
		if err := f.Observer.Table(item.table).Where(item.where, item.args...).Delete(map[string]any{}).Error; err != nil {
			t.Errorf("cleanup %s: %v", item.table, err)
		}
		var remaining int64
		if err := f.Observer.Table(item.table).Where(item.where, item.args...).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("%s remaining=%d: %v", item.table, remaining, err)
		}
	}
	for _, table := range []string{"charge_event_log", "event_outbox"} {
		where, arg := "charge_order_id=?", any(f.ID)
		if table == "event_outbox" {
			where, arg = "event_id IN ?", []string{f.StartCommand, f.StopCommand}
		}
		var remaining int64
		if err := f.Observer.Table(table).Where(where, arg).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("%s remaining=%d: %v", table, remaining, err)
		}
	}
}

// TestRegisteredStartResultPersistsAndReplaysAllLifecycleFacts 使用真实注册端点、专用客户端及独立观察池验证成功起充完整提交与同号重放。
// 仅提交生命周期和通知记录，不执行实际微信、TCP 或计费算法；缺卡操作的 0 行不能错误拒绝成功扫码启动。
func TestRegisteredStartResultPersistsAndReplaysAllLifecycleFacts(t *testing.T) {
	f := newLifecycleAtomicFixture(t, false)
	router := httpapi.NewRouter()
	(centralhttp.StartResultAPI{Service: start.StartResultService{DB: f.Writer}, ServiceToken: "lifecycle-atomic-token"}).Register(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "lifecycle-atomic-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, replayed := range []bool{false, true} {
		reply, err := client.SubmitStartResult(context.Background(), f.startRequest())
		if err != nil || reply != (centralcontract.ResultReply{Accepted: true, Replayed: replayed}) {
			t.Fatalf("actual start reply=%+v want replayed=%v: %v", reply, replayed, err)
		}
		f.assertState(t, false, true)
	}
}

// TestLifecycleLastOutboxFailureRollsBackPriorWrites 分别在实际启动／结束最终 Outbox INSERT 成功后注入错误或取消 context。
// 外层事务必须回滚所有先前写入；移除事务应留下真实持久化而失败，恢复同请求只提交一次，不用 stub 业务结果。
func TestLifecycleLastOutboxFailureRollsBackPriorWrites(t *testing.T) {
	for _, phase := range []struct {
		name     string // 用例阶段名称，仅用于独立子测试定位。
		charging bool   // false 为成功启动，true 为结束已有充电订单。
	}{{"start", false}, {"finish", true}} {
		for _, mode := range []string{"storage-error", "cancel"} {
			t.Run(phase.name+"/"+mode, func(t *testing.T) {
				f := newLifecycleAtomicFixture(t, phase.charging)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				injected := errors.New("lifecycle last outbox injected failure")
				var observed int64
				var hookErr error
				removed := false
				hook := "lifecycle-last-outbox-" + uuid.NewString()
				command := f.StartCommand
				if phase.charging {
					command = f.StopCommand
				}
				if err := f.Writer.Callback().Create().After("gorm:commit_or_rollback_transaction").Register(hook, func(tx *gorm.DB) {
					if tx.Statement.Table != "event_outbox" || tx.Error != nil {
						return
					}
					hookErr = tx.Session(&gorm.Session{NewDB: true}).Table("event_outbox").Where("event_id=?", command).Count(&observed).Error
					if hookErr != nil || observed != 1 {
						tx.AddError(fmt.Errorf("last real INSERT not visible: count=%d: %v", observed, hookErr))
						return
					}
					if mode == "cancel" {
						cancel()
					} else {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if removed {
						return
					}
					if err := f.Writer.Callback().Create().Remove(hook); err != nil {
						t.Errorf("remove create hook: %v", err)
					}
				})
				apply := func() (bool, error) {
					if phase.charging {
						return (finish.EndResultService{DB: f.Writer}).Apply(ctx, f.endResult())
					}
					return (start.StartResultService{DB: f.Writer}).Apply(ctx, start.StartResult(f.startRequest()))
				}
				replayed, err := apply()
				if observed != 1 || hookErr != nil || replayed || err == nil {
					t.Fatalf("post-real-INSERT failure replay=%v observed=%d hook=%v error=%v", replayed, observed, hookErr, err)
				}
				if mode == "storage-error" && !errors.Is(err, injected) {
					t.Fatalf("storage cause lost: %v", err)
				}
				if mode == "cancel" && !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("original context not canceled: %v", ctx.Err())
				}
				f.assertState(t, phase.charging, false)
				if err := f.Writer.Callback().Create().Remove(hook); err != nil {
					t.Fatal(err)
				}
				removed = true
				ctx = context.Background()
				if replayed, err := apply(); err != nil || replayed {
					t.Fatalf("same original request recovery=%v: %v", replayed, err)
				}
				f.assertState(t, phase.charging, true)
			})
		}
	}
}

// TestCentralEndOwnerMismatchRollsBackReceiptAndCompletion 提交另一订单占用同端口，旧结束必须 DELETE0 后冲突且回滚先写结束凭据／订单。
// 直接调用真实 EndResultService 验证中央四条件释放，不以 gateway 迟到回执回归代替；私有新 owner 和其订单已保存状态不改变。
func TestCentralEndOwnerMismatchRollsBackReceiptAndCompletion(t *testing.T) {
	f := newLifecycleAtomicFixture(t, true)
	if err := f.Writer.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("charge_order").Create(map[string]any{"id": f.OtherOwner, "order_no": "C-LA-" + uuid.NewString(), "user_id": f.ID, "device_id": f.DeviceID, "port_no": 1, "status": "charging", "started_at": f.EndedAt, "created_month": f.Month}).Error; err != nil {
			return err
		}
		return tx.Table("active_port_charge").Where("port_id=? AND charge_order_id=?", f.PortID, f.ID).Update("charge_order_id", f.OtherOwner).Error
	}); err != nil {
		t.Fatal(err)
	}
	var deleted int64 = -1
	var beforeDeleteReceipt int64
	var oracleErr error
	hook := "lifecycle-owner-delete-" + uuid.NewString()
	if err := f.Writer.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "active_port_charge" || tx.Error != nil {
			return
		}
		deleted = tx.RowsAffected
		oracleErr = tx.Session(&gorm.Session{NewDB: true}).Table("charge_end_receipt").Where("charge_order_id=?", f.ID).Count(&beforeDeleteReceipt).Error
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Writer.Callback().Delete().Remove(hook); err != nil {
			t.Errorf("remove delete hook: %v", err)
		}
	})
	replayed, err := (finish.EndResultService{DB: f.Writer}).Apply(context.Background(), f.endResult())
	if replayed || !errors.Is(err, finish.ErrEndResultConflict) || deleted != 0 || beforeDeleteReceipt != 1 || oracleErr != nil {
		t.Fatalf("central owner guard replay=%v error=%v deleted=%d actual prior receipt=%d: %v", replayed, err, deleted, beforeDeleteReceipt, oracleErr)
	}
	f.assertCount(t, "charge_end_receipt", "charge_order_id=?", 0, f.ID)
	f.assertCount(t, "charge_billing_job", "charge_order_id=?", 0, f.ID)
	f.assertCount(t, "charge_event_log", "charge_order_id=?", 0, f.ID)
	f.assertCount(t, "event_outbox", "event_id IN ?", 0, []string{f.StartCommand, f.StopCommand})
	f.assertCount(t, "charge_order", "id=? AND created_month=? AND status='charging' AND ended_at IS NULL AND charged_kwh IS NULL AND charged_seconds IS NULL", 1, f.ID, f.Month)
	f.assertCount(t, "charge_order", "id=? AND created_month=? AND status='charging' AND ended_at IS NULL", 1, f.OtherOwner, f.Month)
	f.assertCount(t, "active_port_charge", "port_id=? AND device_id=? AND port_no=1 AND charge_order_id=?", 1, f.PortID, f.DeviceID, f.OtherOwner)
}
