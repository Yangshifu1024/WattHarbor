//go:build integration

package export

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TestExportDeviceScopeIntersection 使用真库验证仅厂商、仅站点及两维交集，不因缺一维扩大到全库。
func TestExportDeviceScopeIntersection(t *testing.T) {
	s, _, first, second := exportFixture(t)
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	ids := []string{"EX" + tag[:28], "EY" + tag[:28]}
	t.Cleanup(func() {
		if err := s.DB.Exec("DELETE FROM device_meta WHERE device_id IN (?,?)", ids[0], ids[1]).Error; err != nil {
			t.Error(err)
		}
	})
	for i, station := range []uint64{first, second} {
		if err := s.DB.Table("device_meta").Create(map[string]any{"device_id": ids[i], "station_id": station, "vendor_id": station, "protocol_adapter": "dc589", "status": "enabled"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	key := "export-device-" + tag
	if err := s.DB.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_meta" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: "device_id", Values: []any{ids[0], ids[1]}}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Callback().Query().Remove(key) })
	for _, scenario := range []struct {
		scope operations.DataScope // 真实管理范围维度
		want  int                  // fixture匹配数量
	}{
		{operations.DataScope{VendorIDs: []uint64{first}}, 1},
		{operations.DataScope{StationIDs: []uint64{first}}, 1},
		{operations.DataScope{StationIDs: []uint64{first}, VendorIDs: []uint64{second}}, 0},
		{operations.DataScope{StationIDs: []uint64{first}, VendorIDs: []uint64{first}}, 1},
	} {
		rows, err := s.repo().QueryExportSource(t.Context(), "devices", scenario.scope, nil, 3)
		if err != nil || len(rows) != scenario.want {
			t.Fatalf("scope %+v rows %+v %v", scenario.scope, rows, err)
		}
		if len(rows) == 1 && stringifyCell(rows[0]["device_id"]) != ids[0] {
			t.Fatalf("wrong device %+v", rows)
		}
	}
}

// TestExportScopeAndMaskCurrentInOldRR 先建立旧范围／mask快照，再在另一池提交收窄，下载必须当前拒绝。
func TestExportScopeAndMaskCurrentInOldRR(t *testing.T) {
	s, audit, first, second := exportFixture(t)
	ctx := context.Background()
	result, err := s.Create(ctx, audit, CreateInput{RequestID: uuid.NewString(), Resource: "stations"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil {
		t.Fatal(err)
	}
	other := secondExportDB(t, first, second)
	for _, isMask := range []bool{false, true} {
		old := other.WithContext(ctx).Begin()
		if old.Error != nil {
			t.Fatal(old.Error)
		}
		func() {
			defer old.Rollback()
			if isMask {
				mask, err := ops.LoadFieldMask(ctx, old, audit.Actor.RoleID)
				if err != nil || mask.Hide("station", "name") {
					t.Fatalf("initial mask %v %v", mask, err)
				}
				if err := s.DB.Exec("INSERT INTO admin_field_mask(role_id,resource,field) VALUES (?,'station','name')", audit.Actor.RoleID).Error; err != nil {
					t.Fatal(err)
				}
				mask, err = ops.LoadFieldMask(ctx, old, audit.Actor.RoleID)
				if err != nil || mask.Hide("station", "name") {
					t.Fatalf("not stale mask %v %v", mask, err)
				}
			} else {
				scope, err := ops.LoadDataScope(ctx, old, audit.Actor)
				if err != nil || !scope.Unrestricted {
					t.Fatalf("initial scope %+v %v", scope, err)
				}
				if err := s.DB.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES (?,'station',?,?)", audit.Actor.ID, first, audit.Actor.ID).Error; err != nil {
					t.Fatal(err)
				}
				scope, err = ops.LoadDataScope(ctx, old, audit.Actor)
				if err != nil || !scope.Unrestricted {
					t.Fatalf("not stale scope %+v %v", scope, err)
				}
			}
			stale := s
			stale.DB = old
			var denied *ForbiddenError
			if _, err := stale.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &denied) {
				t.Fatalf("old RR admission mask=%v: %v", isMask, err)
			}
		}()
		if err := s.DB.Exec("DELETE FROM admin_data_scope WHERE admin_user_id=?", audit.Actor.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Exec("DELETE FROM admin_field_mask WHERE role_id=?", audit.Actor.RoleID).Error; err != nil {
			t.Fatal(err)
		}
	}
}
