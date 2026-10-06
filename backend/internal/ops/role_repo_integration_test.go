//go:build integration

package ops

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// TestRoleUserCountDoesNotLockOtherRoles 在两条真实连接中验证角色账号当前读不扫描锁定无关角色的账号。
// 同角色的停用账号仍计入引用，软删除账号不计入；夹具按回填主键清理，不修改现有账号或角色。
func TestRoleUserCountDoesNotLockOtherRoles(t *testing.T) {
	first, closeFirst := testConn(t)
	t.Cleanup(closeFirst)
	second, closeSecond := testConn(t)
	t.Cleanup(closeSecond)
	roles := make([]RoleRow, 2)
	for i := range roles {
		roles[i] = RoleRow{Code: "role_lock_" + uuid.NewString(), Name: "角色引用锁夹具"}
		if err := first.Table("role").Select("code", "name", "is_builtin").Create(&roles[i]).Error; err != nil {
			t.Fatal(err)
		}
		id := roles[i].ID
		t.Cleanup(func() {
			if err := first.Table("role").Where("id=?", id).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := first.Table("role").Where("id=?", id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("role fixture remaining=%d err=%v", n, err)
			}
		})
	}
	// account 是角色引用锁测试写入 admin_user 的私有夹具，区分启用、停用和软删除引用；占位口令不用于登录。
	type account struct {
		ID           uint64     `gorm:"primaryKey"` // 插入回填的私有账号主键。
		Username     string     // 本测试独有用户名，不依赖共享用户名。
		PasswordHash string     // 占位存储值，不用于登录或校验口令。
		RoleID       uint64     // 本夹具所属角色，目标与无关角色分离。
		Status       string     // 原账号状态；停用账号仍引用角色。
		DeletedAt    *time.Time // 原软删除事实；已删除账号不阻止角色删除。
	}
	now := time.Now().UTC()
	rows := []account{
		{Username: uuid.NewString(), PasswordHash: "fixture", RoleID: roles[0].ID, Status: "active"},
		{Username: uuid.NewString(), PasswordHash: "fixture", RoleID: roles[1].ID, Status: "active"},
		{Username: uuid.NewString(), PasswordHash: "fixture", RoleID: roles[1].ID, Status: "disabled"},
		{Username: uuid.NewString(), PasswordHash: "fixture", RoleID: roles[1].ID, Status: "active", DeletedAt: &now},
	}
	for i := range rows {
		if err := first.Table("admin_user").Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		id := rows[i].ID
		t.Cleanup(func() {
			if err := first.Table("admin_user").Where("id=?", id).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := first.Table("admin_user").Where("id=?", id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("account fixture remaining=%d err=%v", n, err)
			}
		})
	}
	locker := first.Begin()
	if locker.Error != nil {
		t.Fatal(locker.Error)
	}
	defer locker.Rollback()
	var other account
	if err := locker.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", rows[0].ID).Take(&other).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reader := second.WithContext(ctx).Begin()
	if reader.Error != nil {
		t.Fatal(reader.Error)
	}
	defer reader.Rollback()
	count, err := (RoleRepo{DB: reader}).CountRoleUsersInTx(reader, roles[1].ID)
	if err != nil || count != 2 {
		t.Fatalf("role count blocked by unrelated account: count=%d err=%v", count, err)
	}
}
