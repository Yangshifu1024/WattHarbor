//go:build integration

package pricing

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/testfixture"
)

// pricingConn 打开独立 MySQL 连接，配置或连接失败即终止测试，不跳过，结束后关闭。
func pricingConn(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("TEST_MYSQL_URL")
	if url == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// pricingScheme 构造支持 DC589 的时长套餐，金额单位分，时长单位分钟。
func pricingScheme() charging.Scheme {
	return charging.Scheme{Name: "原价套餐", Packages: []charging.Package{{ID: 1, Name: "一小时", Mode: "duration", PriceCents: 400, Minutes: 60}}}
}

// pricingActor 返回授权测试账号；真实会话和权限查询不在该测试范围。
func pricingActor(id uint64, request string) ops.AuditContext {
	return ops.AuditContext{Actor: operations.Profile{ID: id, Username: "pricing-proof", Permissions: []string{"pricing.rule.create", "pricing.rule.update"}}, RequestID: request}
}

// seedPricingTarget 创建本测试站点和设备，调用方负责回滚或按测试 ID 清理。
func seedPricingTarget(db *gorm.DB, id uint64, device string) error {
	if err := db.Table("station").Create(map[string]any{"id": id, "name": device, "longitude": 0, "latitude": 0, "status": "active"}).Error; err != nil {
		return err
	}
	return db.Table("device_meta").Create(map[string]any{"device_id": device, "station_id": id, "vendor_id": id, "protocol_adapter": "dc589", "status": "enabled"}).Error
}

// TestSchemePublicationPreservesReplayScopeVersionsAndAudit 用真库验证模板、范围、请求重放和审计同事务。
// 授权使用夹具，外层事务回滚，不发设备指令；已发布 JSON 不随模板变化。
func TestSchemePublicationPreservesReplayScopeVersionsAndAudit(t *testing.T) {
	db := pricingConn(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	device := "PR" + rand.Text()
	request := uuid.NewString()
	if err := seedPricingTarget(tx, id, device); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	audit := pricingActor(id, request)
	scope := operations.DataScope{Unrestricted: true}
	schemes := SchemeService{DB: tx}
	publications := PublicationService{DB: tx}
	created, err := schemes.Save(ctx, audit, 0, SchemeInput{Scheme: pricingScheme()})
	if err != nil {
		t.Fatal(err)
	}
	in := PublishInput{RequestID: request, StationID: id, DeviceID: device, TemplateID: created.ID, TemplateVersion: created.Version}
	out, err := publications.Publish(ctx, audit, scope, in)
	if err != nil || out.Replayed || out.Version != 1 {
		t.Fatalf("publish=%+v %v", out, err)
	}
	var frozen string
	if err := tx.Table("pricing_rule").Where("id=?", out.ID).Pluck("spec_json", &frozen).Error; err != nil {
		t.Fatal(err)
	}
	updated := pricingScheme()
	updated.Packages[0].PriceCents = 700
	if _, err := schemes.Save(ctx, audit, created.ID, SchemeInput{Scheme: updated, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	prior, err := publications.Publish(ctx, audit, scope, in)
	if err != nil || !prior.Replayed || prior.ID != out.ID || prior.Version != out.Version {
		t.Fatalf("replay=%+v %v", prior, err)
	}
	var now string
	if err := tx.Table("pricing_rule").Where("id=?", out.ID).Pluck("spec_json", &now).Error; err != nil || now != frozen {
		t.Fatalf("published snapshot changed: %s %v", now, err)
	}
	changed := in
	changed.ExpectedVersion = 1
	if _, err := publications.Publish(ctx, audit, scope, changed); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed replay=%v", err)
	}
	changed.RequestID = uuid.NewString()
	if _, err := publications.Publish(ctx, audit, scope, changed); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("old template version=%v", err)
	}
	changed.TemplateVersion = 2
	blocked := operations.DataScope{StationIDs: []uint64{id + 1}}
	if _, err := publications.Publish(ctx, audit, blocked, changed); !errors.Is(err, operations.ErrForbidden) {
		t.Fatalf("station scope=%v", err)
	}
	blocked = operations.DataScope{VendorIDs: []uint64{id + 1}}
	if _, err := publications.Publish(ctx, audit, blocked, changed); !errors.Is(err, operations.ErrForbidden) {
		t.Fatalf("vendor scope=%v", err)
	}
	stationDefault := changed
	stationDefault.DeviceID = ""
	if _, err := publications.Publish(ctx, audit, operations.DataScope{VendorIDs: []uint64{id}}, stationDefault); !errors.Is(err, operations.ErrForbidden) {
		t.Fatalf("vendor station default=%v", err)
	}
	if err := tx.Table("device_meta").Where("device_id=?", device).Update("protocol_adapter", "unsupported").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := publications.Publish(ctx, audit, scope, changed); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("unsupported device=%v", err)
	}
	if err := tx.Table("device_meta").Where("device_id=?", device).Update("protocol_adapter", "dc589").Error; err != nil {
		t.Fatal(err)
	}
	// 规则和请求记录保存后注入故障，须连同被停用旧规则一起回滚。
	key := "pricing_audit_" + rand.Text()
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(errors.New("audit storage unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := publications.Publish(ctx, audit, scope, changed); err == nil {
		t.Fatal("audit failure accepted")
	}
	if err := db.Callback().Create().Remove(key); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := tx.Table("pricing_rule").Where("station_id=?", id).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("partial rules=%d %v", n, err)
	}
	if err := tx.Table("pricing_publication").Where("request_id=?", changed.RequestID).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("partial receipt=%d %v", n, err)
	}
	var status string
	if err := tx.Table("pricing_rule").Where("id=?", out.ID).Pluck("status", &status).Error; err != nil || status != "active" {
		t.Fatalf("original rule=%s %v", status, err)
	}
	if err := tx.Table("audit_log").Where("request_id=?", request).Count(&n).Error; err != nil || n != 3 {
		t.Fatalf("audits=%d %v", n, err)
	}
}

// TestConcurrentPublicationReturnsOriginalResult 验证两个真库连接同请求发布返回相同主键，只有一条规则、请求和审计。
// 按锁闸门固定顺序，仅清理本测试数据并检查零残留。
func TestConcurrentPublicationReturnsOriginalResult(t *testing.T) {
	setup := pricingConn(t)
	first := pricingConn(t)
	second := pricingConn(t)
	id := testfixture.NewID()
	device := "PC" + rand.Text()
	request := uuid.NewString()
	defer func() {
		for _, q := range []struct {
			table string /* 本测试实际提交的表 */
			where string /* 测试唯一身份 */
			arg   any    /* 查询筛选值 */
		}{
			{"audit_log", "request_id=?", request}, {"pricing_publication", "request_id=?", request}, {"pricing_rule", "station_id=?", id}, {"device_meta", "device_id=?", device}, {"station", "id=?", id},
		} {
			if err := setup.Table(q.table).Where(q.where, q.arg).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := setup.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	if err := seedPricingTarget(setup, id, device); err != nil {
		t.Fatal(err)
	}
	locked, release, trying := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var lockOnce, attemptOnce, releaseOnce sync.Once
	pass := func() { releaseOnce.Do(func() { close(release) }) }
	defer pass()
	key := "pricing_lock_" + rand.Text()
	if err := first.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				lockOnce.Do(func() { close(locked); <-release })
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(key)
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				attemptOnce.Do(func() { close(trying) })
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key)
	// answer 汇总发布结果与错误，规则、请求和审计数量另查真库。
	type answer struct {
		result PublicationResult /* 发布结果 */
		err    error             /* 事务执行结果 */
	}
	scheme := pricingScheme()
	in := PublishInput{RequestID: request, StationID: id, DeviceID: device, Scheme: &scheme}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	call := func(db *gorm.DB) chan answer {
		ch := make(chan answer, 1)
		go func() {
			out, err := (PublicationService{DB: db}).Publish(ctx, pricingActor(id, request), operations.DataScope{Unrestricted: true}, in)
			ch <- answer{out, err}
		}()
		return ch
	}
	a := call(first)
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("first station lock not reached")
	}
	b := call(second)
	select {
	case <-trying:
	case <-ctx.Done():
		t.Fatal("second station lock not attempted")
	}
	pass()
	one, two := <-a, <-b
	if one.err != nil || two.err != nil || one.result.Replayed || !two.result.Replayed || one.result.ID != two.result.ID || one.result.Version != two.result.Version {
		t.Fatalf("first=%+v err=%v second=%+v err=%v", one.result, one.err, two.result, two.err)
	}
	for _, q := range []struct {
		table string /* 本次规则及审计所属表 */
		where string /* 目标或请求号 */
		arg   any    /* 查询筛选值 */
	}{{"pricing_rule", "station_id=?", id}, {"pricing_publication", "request_id=?", request}, {"audit_log", "request_id=?", request}} {
		var n int64
		if err := setup.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 1 {
			t.Errorf("%s count=%d %v", q.table, n, err)
		}
	}
}
