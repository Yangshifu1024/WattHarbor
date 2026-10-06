//go:build integration

package pricing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// cleanupPricingIsolation 删除 stations、devices 和 requests 对应的本测试造数，返回存储或残留错误。
// 审计及规则先非锁定位完整主键后逐行删除，其余表按已知主键删除；调用前须等待所有发布事务退出。
func cleanupPricingIsolation(db *gorm.DB, stations []uint64, devices, requests []string) error {
	var audits []struct {
		ID           uint64    // 本测试请求审计的记录主键。
		CreatedMonth time.Time // 审计分区主键的原月份。
	}
	if err := db.Table("audit_log").Select("id,created_month").Where("request_id IN ?", requests).
		Order("id,created_month").Find(&audits).Error; err != nil {
		return err
	}
	for _, row := range audits {
		if err := db.Table("audit_log").Where("id=? AND created_month=? AND request_id IN ?", row.ID, row.CreatedMonth, requests).
			Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	for _, request := range requests {
		if err := db.Table("pricing_publication").Where("request_id=?", request).Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	var rules []uint64
	if err := db.Table("pricing_rule").Where("station_id IN ?", stations).Order("id").Pluck("id", &rules).Error; err != nil {
		return err
	}
	for _, id := range rules {
		if err := db.Table("pricing_rule").Where("id=? AND station_id IN ?", id, stations).Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	for _, device := range devices {
		if err := db.Table("device_meta").Where("device_id=?", device).Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	for _, station := range stations {
		if err := db.Table("station").Where("id=?", station).Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	for _, row := range []struct {
		table string // 已清理的测试表。
		where string // 原测试身份条件，只用于非锁残留检查。
		arg   any    // 当前测试唯一身份集合。
	}{
		{"audit_log", "request_id IN ?", requests}, {"pricing_publication", "request_id IN ?", requests},
		{"pricing_rule", "station_id IN ?", stations}, {"device_meta", "device_id IN ?", devices}, {"station", "id IN ?", stations},
	} {
		var count int64
		if err := db.Table(row.table).Where(row.where, row.arg).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("pricing isolation fixture %s remaining=%d", row.table, count)
		}
	}
	return nil
}

// TestPricingPublicationDoesNotBlockAnotherStation 用 t 管理独立 MySQL 连接，验证站点隔离及实际发布／继承事务。
// RR 第一事务完成停用阶段后仍持锁，第二站点首次发布须先完成；空目标及已有规则均覆盖，不靠 sleep 排序。
// 原作用域范围 UPDATE 会阻挡第二站点插入直至期限；身份为 fixture，规则、凭据、审计及锁均真实执行。
func TestPricingPublicationDoesNotBlockAnotherStation(t *testing.T) {
	for _, tc := range []struct {
		name     string // 发布或继承的空目标／现有规则场景。
		inherit  bool   // true 调用 Assign，否则调用 Publish。
		existing bool   // 是否先提交目标设备的 active 规则。
	}{
		{"publish-empty", false, false}, {"publish-existing", false, true},
		{"inherit-empty", true, false}, {"inherit-existing", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup, first, second := pricingConn(t), pricingConn(t), pricingConn(t)
			station := testfixture.NewID()
			stations := []uint64{station, station + 1}
			devices := []string{"PA" + rand.Text(), "PB" + rand.Text()}
			requests := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
			defer func() {
				if err := cleanupPricingIsolation(setup, stations, devices, requests); err != nil {
					t.Error(err)
				}
			}()
			for i, id := range stations {
				if err := seedPricingTarget(setup, id, devices[i]); err != nil {
					t.Fatal(err)
				}
			}
			scheme := pricingScheme().Normalized()
			scope := operations.DataScope{Unrestricted: true}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			seed := func(request, device string) {
				in := PublishInput{RequestID: request, StationID: station, DeviceID: device, Scheme: &scheme}
				if _, err := (PublicationService{DB: setup}).Publish(ctx, pricingActor(station, request), scope, in); err != nil {
					t.Fatal(err)
				}
			}
			if tc.inherit {
				seed(requests[2], "")
			}
			version := uint32(0)
			if tc.existing {
				seed(requests[3], devices[0])
				version = 1
			}
			gateTable := "pricing_rule"
			if tc.inherit {
				gateTable = "audit_log"
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once, released sync.Once
			pass := func() { released.Do(func() { close(release) }) }
			key := "pricing_isolation_" + rand.Text()
			if err := first.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
				if q.Statement.Table == gateTable {
					once.Do(func() { close(entered); <-release })
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer first.Callback().Create().Remove(key)
			result := make(chan error, 1)
			go func() {
				result <- first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					service := PublicationService{DB: tx}
					audit := pricingActor(station, requests[0])
					if tc.inherit {
						return service.Assign(ctx, audit, scope, station, InheritInput{DeviceID: devices[0], ExpectedVersion: version})
					}
					_, err := service.Publish(ctx, audit, scope, PublishInput{RequestID: requests[0], StationID: station, DeviceID: devices[0], ExpectedVersion: version, Scheme: &scheme})
					return err
				}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			}()
			// 放行及等待先于移除回调和清理造数，失败路径也不会留下持锁协程。
			defer func() {
				pass()
				if err := <-result; err != nil {
					t.Errorf("first station transaction: %v", err)
				}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first station did not reach post-disable gate")
			}
			otherCtx, otherCancel := context.WithTimeout(ctx, 2*time.Second)
			defer otherCancel()
			otherInput := PublishInput{RequestID: requests[1], StationID: stations[1], DeviceID: devices[1], Scheme: &scheme}
			var other PublicationResult
			err := second.WithContext(otherCtx).Transaction(func(tx *gorm.DB) error {
				var err error
				other, err = (PublicationService{DB: tx}).Publish(otherCtx, pricingActor(stations[1], requests[1]), scope, otherInput)
				return err
			}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if err != nil || other.ID == 0 || other.Version != 1 || other.Replayed {
				t.Fatalf("other station blocked while first holds transaction: result=%+v err=%v", other, err)
			}
			var persisted struct {
				Station uint64 `gorm:"column:station_id"` // 第二站点已提交规则的真实归属。
				Version uint32 `gorm:"column:version"`    // 首次发布必须保持版本 1。
				Spec    []byte `gorm:"column:spec_json"`  // 原规范化方案的持久快照。
			}
			if err := setup.Table("pricing_rule").Where("id=?", other.ID).Take(&persisted).Error; err != nil || persisted.Station != stations[1] || persisted.Version != 1 {
				t.Fatalf("other committed rule=%+v err=%v", persisted, err)
			}
			var expectedSpec any
			if err := json.Unmarshal(persisted.Spec, &expectedSpec); err != nil || expectedSpec == nil {
				t.Fatalf("other committed snapshot=%s err=%v", persisted.Spec, err)
			}
		})
	}
}
