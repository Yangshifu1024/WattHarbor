//go:build integration

package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/platform/auth"
)

// managementFixture 为自身自定义角色授予九项权限，并创建两条真库连接及审计参数；不修改内置角色或初始管理员。
func managementFixture(t *testing.T) (*gorm.DB, *gorm.DB, SessionRepo, AuditContext, *auth.JWT) {
	t.Helper()
	first, second, repo, account, _, jwt := securityFixture(t)
	for _, code := range []string{"admin_user.read", "admin_user.create", "admin_user.update", "admin_user.delete", "admin_user.reset_password", "role.create", "role.update", "role.delete", "order.read"} {
		var permission uint64
		if err := first.Table("permission").Where("code=?", code).Pluck("id", &permission).Error; err != nil || permission == 0 {
			t.Fatalf("permission %s %d %v", code, permission, err)
		}
		if err := first.Table("role_permission").Create(map[string]any{"role_id": account.RoleID, "permission_id": permission}).Error; err != nil {
			t.Fatal(err)
		}
	}
	profile, err := (AccountRepo{DB: first}).Profile(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	return first, second, repo, AuditContext{Actor: profile, ClientIP: "management-fixture", RequestID: uuid.NewString()}, jwt
}

// cleanupManagementRole 仅清理本测试角色和权限，并检查零残留。
func cleanupManagementRole(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	if id == 0 {
		return
	}
	t.Cleanup(func() {
		if err := db.Table("role_permission").Where("role_id=?", id).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		if err := db.Table("role").Where("id=?", id).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var count int64
		if err := db.Table("role").Where("id=?", id).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("role cleanup %d %v", count, err)
		}
	})
}

// cleanupManagementUser 仅清理本测试账号并检查零残留，审计由操作者夹具清理。
func cleanupManagementUser(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	if id == 0 {
		return
	}
	t.Cleanup(func() {
		if err := db.Table("admin_user").Where("id=?", id).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var count int64
		if err := db.Table("admin_user").Where("id=?", id).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("user cleanup %d %v", count, err)
		}
	})
}

// TestManagementAccountRoleTransactionsAndSafeAudit 使用真实 SQL／Redis 验证管理全流程、实时权限与安全审计。
// 同角色回传不撤销，停用再启用不得恢复旧签名，重置解除临时锁定；MFA 确认必须使用服务端登记材料。
func TestManagementAccountRoleTransactionsAndSafeAudit(t *testing.T) {
	db, _, repo, audit, jwt := managementFixture(t)
	ctx := context.Background()
	roles, accounts := RoleService{DB: db}, AccountService{DB: db}
	code := "proof_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	role, err := roles.Create(ctx, audit, RoleInput{Code: code, Name: "管理测试", Permissions: []string{"order.read", "admin_user.read"}})
	cleanupManagementRole(t, db, role)
	if err != nil {
		t.Fatal(err)
	}
	password := "Created!ManagedPassword2026"
	input := AccountCreateInput{Username: "managed-" + uuid.NewString(), DisplayName: "原显示名", Phone: "", Password: password, RoleID: role}
	id, err := accounts.Create(ctx, audit, input)
	cleanupManagementUser(t, db, id)
	if err != nil {
		t.Fatal(err)
	}
	var original AdminAccount
	if err := db.Where("id=?", id).Take(&original).Error; err != nil || bcrypt.CompareHashAndPassword([]byte(original.PasswordHash), []byte(password)) != nil {
		t.Fatalf("create hash %v", err)
	}
	sid, _, err := repo.Create(ctx, original.Identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.RevokeSession(ctx, sid) })
	profile, err := (AccountRepo{DB: db}).Profile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	token, err := (JWTAccess{JWT: jwt}).Sign(profile, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sessions := SessionService{Accounts: AccountRepo{DB: db}, Sessions: repo, Tokens: JWTAccess{JWT: jwt}}
	if _, _, err := sessions.Authenticate(ctx, token, "admin_user.read"); err != nil {
		t.Fatal(err)
	}
	if locked, err := roles.Update(ctx, audit, role, RoleInput{Code: code, Name: "修改名称", Permissions: []string{"order.read"}}); err != nil || locked {
		t.Fatalf("role update %v %v", locked, err)
	}
	if _, _, err := sessions.Authenticate(ctx, token, "admin_user.read"); !errors.Is(err, ErrPermission) {
		t.Fatalf("permission removal %v", err)
	}
	if _, _, err := sessions.Authenticate(ctx, token, "order.read"); err != nil {
		t.Fatalf("remaining permission %v", err)
	}
	name := "普通资料"
	if revoked, err := accounts.Update(ctx, audit, id, AccountUpdateInput{DisplayName: &name, RoleID: &role}); err != nil || revoked {
		t.Fatalf("same role %v %v", revoked, err)
	}
	disabled := "disabled"
	if revoked, err := accounts.Update(ctx, audit, id, AccountUpdateInput{Status: &disabled}); err != nil || !revoked {
		t.Fatalf("disable %v %v", revoked, err)
	}
	active := "active"
	if revoked, err := accounts.Update(ctx, audit, id, AccountUpdateInput{Status: &active}); err != nil || !revoked {
		t.Fatalf("enable %v %v", revoked, err)
	}
	if _, _, err := sessions.Authenticate(ctx, token, ""); !errors.Is(err, operations.ErrCredentials) {
		t.Fatalf("old session revived %v", err)
	}
	if err := db.Table("admin_user").Where("id=?", id).Updates(map[string]any{"status": "locked", "failed_login_count": 5, "locked_until": time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	reset := "Reset!ManagedPassword2026"
	if err := accounts.ResetPassword(ctx, audit, id, reset); err != nil {
		t.Fatal(err)
	}
	var resetAccount AdminAccount
	if err := db.Where("id=?", id).Take(&resetAccount).Error; err != nil || resetAccount.Status != "active" || resetAccount.LockedUntil.Valid || resetAccount.FailedLoginCount != 0 || bcrypt.CompareHashAndPassword([]byte(resetAccount.PasswordHash), []byte(reset)) != nil {
		t.Fatalf("reset state %+v %v", resetAccount, err)
	}
	if err := db.Table("admin_user").Where("id=?", id).Update("status", "locked").Error; err != nil {
		t.Fatal(err)
	}
	if err := accounts.Unlock(ctx, audit, id); err != nil {
		t.Fatal(err)
	}
	enrolled, err := accounts.MFA(ctx, audit, id, ManagedMFAInput{Action: "enrol"})
	if err != nil || enrolled.Secret == "" || enrolled.Code == "" || enrolled.Enabled {
		t.Fatalf("enrol %v", err)
	}
	chosen, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	chosenCode, err := auth.TOTPCode(chosen, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.MFA(ctx, audit, id, ManagedMFAInput{Action: "confirm", Secret: chosen, Code: chosenCode}); err == nil {
		t.Fatal("client-chosen secret accepted")
	}
	currentCode, err := auth.TOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := accounts.MFA(ctx, audit, id, ManagedMFAInput{Action: "confirm", Secret: enrolled.Secret, Code: currentCode}); err != nil || !reply.Enabled || reply.ID != id {
		t.Fatalf("confirm %v", err)
	}
	if reply, err := accounts.MFA(ctx, audit, id, ManagedMFAInput{Action: "disable"}); err != nil || reply.Enabled {
		t.Fatalf("disable %v", err)
	}
	if err := roles.Delete(ctx, audit, role); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("assigned role delete %v", err)
	}
	if err := accounts.Delete(ctx, audit, id); err != nil {
		t.Fatal(err)
	}
	if err := roles.Delete(ctx, audit, role); err != nil {
		t.Fatal(err)
	}
	var snapshots []struct {
		Before string `gorm:"column:before_json"` // 变更前安全快照
		After  string `gorm:"column:after_json"`  // 变更后安全快照
	}
	if err := db.Table("audit_log").Where("actor_id=?", audit.Actor.ID).Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 10 {
		t.Fatalf("missing audits %d", len(snapshots))
	}
	for _, row := range snapshots {
		raw := row.Before + row.After
		for _, secret := range []string{"password_hash", "mfa_secret", password, reset, original.PasswordHash, resetAccount.PasswordHash, enrolled.Secret, currentCode, chosen} {
			if strings.Contains(raw, secret) {
				t.Fatal("sensitive material reached audit")
			}
		}
	}
}

// TestManagementAuditFailureRollsBackRoleAndAccount 验证审计失败时账号、角色关联一起回滚；清理只涉及本测试操作者。
func TestManagementAuditFailureRollsBackRoleAndAccount(t *testing.T) {
	db, _, _, audit, _ := managementFixture(t)
	ctx := context.Background()
	roles := RoleService{DB: db}
	code := "rollback_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	failure := errors.New("management audit fixture")
	name := fmt.Sprint("management-audit-", audit.Actor.ID)
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	id, err := roles.Create(ctx, audit, RoleInput{Code: code, Name: "回滚测试", Permissions: []string{"order.read"}})
	db.Callback().Create().Remove(name)
	cleanupManagementRole(t, db, id)
	if !errors.Is(err, failure) {
		t.Fatalf("audit outcome %v", err)
	}
	for _, table := range []string{"role", "role_permission"} {
		column := "id"
		if table == "role_permission" {
			column = "role_id"
		}
		var count int64
		if err := db.Table(table).Where(column+"=?", id).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rollback %s=%d err=%v", table, count, err)
		}
	}
	role, err := roles.Create(ctx, audit, RoleInput{Code: code, Name: "账号回滚", Permissions: []string{"order.read"}})
	cleanupManagementRole(t, db, role)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	user, err := (AccountService{DB: db}).Create(ctx, audit, AccountCreateInput{Username: "rollback-" + uuid.NewString(), Password: "Rollback!Password2026", RoleID: role})
	db.Callback().Create().Remove(name)
	cleanupManagementUser(t, db, user)
	if !errors.Is(err, failure) {
		t.Fatalf("account audit outcome %v", err)
	}
	var count int64
	if err := db.Table("admin_user").Where("id=?", user).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("account rollback=%d %v", count, err)
	}
}

// TestManagementGrantReadsCurrentPermissions 固定旧 RR 快照早于另一真实角色更新，当前权限读取不能再次授予撤销项。
// 撤销由真实 RoleService 提交，旧请求仍持先前的 Profile；不以 HTTP 旧菜单或角色快照替代锁后授权。
func TestManagementGrantReadsCurrentPermissions(t *testing.T) {
	first, second, _, audit, _ := managementFixture(t)
	ctx := context.Background()
	tx := first.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var old []string
	if err := tx.Table("role_permission AS rp").Joins("JOIN permission AS p ON p.id=rp.permission_id").Where("rp.role_id=?", audit.Actor.RoleID).Pluck("p.code", &old).Error; err != nil {
		t.Fatal(err)
	}
	remaining := []string{}
	for _, code := range audit.Actor.Permissions {
		if code != "order.read" {
			remaining = append(remaining, code)
		}
	}
	if _, err := (RoleService{DB: second}).Update(ctx, audit, audit.Actor.RoleID, RoleInput{Code: "fixture_actor", Name: "撤销赋权测试", Permissions: remaining}); err != nil {
		t.Fatal(err)
	}
	code := "denied_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	id, err := (RoleService{DB: tx}).Create(ctx, audit, RoleInput{Code: code, Name: "不应再授予", Permissions: []string{"order.read"}})
	var input *ManagementInputError
	if !errors.As(err, &input) {
		t.Fatalf("revoked grant accepted id=%d err=%v", id, err)
	}
	var count int64
	if err := tx.Table("role").Where("code=?", code).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("denied grant created %d %v", count, err)
	}
}

// TestManagementRoleDeletionUsesCurrentReferences 固定旧 RR 角色引用计数早于真实账号分配，删除须看到新引用而拒绝。
// 创建和角色集合使用实际服务，外层回滚不清空共享角色／权限或开发数据。
func TestManagementRoleDeletionUsesCurrentReferences(t *testing.T) {
	first, second, _, audit, _ := managementFixture(t)
	ctx := context.Background()
	role, err := (RoleService{DB: first}).Create(ctx, audit, RoleInput{Code: "reference_" + strings.ReplaceAll(uuid.NewString(), "-", "_"), Name: "引用当前读", Permissions: []string{"order.read"}})
	cleanupManagementRole(t, first, role)
	if err != nil {
		t.Fatal(err)
	}
	tx := first.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var before int64
	if err := tx.Table("admin_user").Where("role_id=? AND deleted_at IS NULL", role).Count(&before).Error; err != nil || before != 0 {
		t.Fatalf("initial refs=%d %v", before, err)
	}
	user, err := (AccountService{DB: second}).Create(ctx, audit, AccountCreateInput{Username: "reference-" + uuid.NewString(), Password: "Reference!Password2026", RoleID: role})
	cleanupManagementUser(t, first, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := (RoleService{DB: tx}).Delete(ctx, audit, role); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("new reference omitted %v", err)
	}
}

// isolateManagementAdmins 仅限制管理员计数为本测试账号，仍使用真实共享角色锁。
// 保留初始管理员和其他账号，不把测试子集当作全库统计。
func isolateManagementAdmins(t *testing.T, db *gorm.DB, ids []uint64) {
	t.Helper()
	name := fmt.Sprint("management-own-count-", ids[0])
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "admin_user AS u" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: clause.Column{Table: "u", Name: "id"}, Values: []any{ids[0], ids[1]}}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
}

// TestManagementLastCustomerAdminAllRemovalPaths 验证停用、改派和软删都保留最后客户管理员，解除临时锁不复活旧凭据。
// 测试夹具只计自身两名客户管理员，使用真实共享角色行和两连接；bootstrap 保持原样。
func TestManagementLastCustomerAdminAllRemovalPaths(t *testing.T) {
	first, second, _, audit, _ := managementFixture(t)
	ctx := context.Background()
	var rootRole uint64
	if err := first.Table("role").Where("code='customer_admin' AND deleted_at IS NULL").Pluck("id", &rootRole).Error; err != nil || rootRole == 0 {
		t.Fatalf("root %d %v", rootRole, err)
	}
	ids := make([]uint64, 0, 2)
	for i := range 2 {
		status := AdminUserActive
		if i == 1 {
			status = AdminUserDisabled
		}
		account := AdminAccount{Username: "cust-" + uuid.NewString(), PasswordHash: "fixture", RoleID: rootRole, Status: status, AuthVersion: 1}
		if err := first.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, account.ID)
		cleanupManagementUser(t, first, account.ID)
	}
	isolateManagementAdmins(t, first, ids)
	isolateManagementAdmins(t, second, ids)
	lowRole, err := (RoleService{DB: first}).Create(ctx, audit, RoleInput{Code: "custlow_" + strings.ReplaceAll(uuid.NewString(), "-", "_"), Name: "低权限", Permissions: []string{"order.read"}})
	cleanupManagementRole(t, first, lowRole)
	if err != nil {
		t.Fatal(err)
	}
	accounts := AccountService{DB: first}
	disabled := "disabled"
	if _, err := accounts.Update(ctx, audit, ids[0], AccountUpdateInput{Status: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last disabled %v", err)
	}
	if _, err := accounts.Update(ctx, audit, ids[0], AccountUpdateInput{RoleID: &lowRole}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last reassigned %v", err)
	}
	if err := accounts.Delete(ctx, audit, ids[0]); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last deleted %v", err)
	}
	if err := accounts.Unlock(ctx, audit, ids[1]); err != nil {
		t.Fatal(err)
	}
	// 创建旧 RR 快照后另一连接提交第二名停用，当前计数不得漏掉真实状态变化。
	tx := first.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var snapshot []AdminAccount
	if err := tx.Where("id IN ?", ids).Find(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (AccountService{DB: second}).Update(ctx, audit, ids[1], AccountUpdateInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := (AccountService{DB: tx}).Update(ctx, audit, ids[0], AccountUpdateInput{Status: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("old RR removed final active %v", err)
	}
	var row AdminAccount
	if err := tx.Where("id=?", ids[0]).Clauses(clause.Locking{Strength: "SHARE"}).Take(&row).Error; err != nil || row.Status != "active" {
		t.Fatalf("remaining active %s %v", row.Status, err)
	}
	// 锁读出的密码或 MFA 材料仍不能写入管理审计。
	safe, err := (AccountRepo{DB: tx}).LockAdminUser(tx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(safe)
	if err != nil || strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "mfa_secret") {
		t.Fatalf("snapshot %v", err)
	}
}

// TestManagementConcurrentRevocationBeforeAssignment 固定真实角色集合锁顺序，后到账号分配须使用撤销后权限。
// 两个独立连接各执行实际服务；闸门只控制客户管理员角色查询，造数和清理限自身身份。
func TestManagementConcurrentRevocationBeforeAssignment(t *testing.T) {
	first, second, _, audit, _ := managementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	role, err := (RoleService{DB: first}).Create(ctx, audit, RoleInput{Code: "race_" + strings.ReplaceAll(uuid.NewString(), "-", "_"), Name: "并发分配", Permissions: []string{"order.read"}})
	cleanupManagementRole(t, first, role)
	if err != nil {
		t.Fatal(err)
	}
	held, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	firstName, secondName := "management-first-root", "management-second-root"
	rootQuery := func(q *gorm.DB) bool {
		if q.Statement.Table != "role" {
			return false
		}
		where, ok := q.Statement.Clauses["WHERE"]
		return ok && strings.Contains(fmt.Sprint(where.Expression), "customer_admin")
	}
	if err := first.Callback().Query().After("gorm:query").Register(firstName, func(q *gorm.DB) {
		if rootQuery(q) {
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
				q.AddError(ctx.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(firstName)
	if err := second.Callback().Query().Before("gorm:query").Register(secondName, func(q *gorm.DB) {
		if rootQuery(q) {
			close(attempted)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(secondName)
	remaining := []string{}
	for _, code := range audit.Actor.Permissions {
		if code != "order.read" {
			remaining = append(remaining, code)
		}
	}
	updated := make(chan error, 1)
	go func() {
		_, e := (RoleService{DB: first}).Update(ctx, audit, audit.Actor.RoleID, RoleInput{Code: "fixture_actor", Name: "真实撤权", Permissions: remaining})
		updated <- e
	}()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("first root lock not reached")
	}
	// result 汇总账号创建主键和错误，另查数据库确认未授权创建未保存。
	type result struct {
		id  uint64 // 并发创建返回的真实账号主键
		err error  // 创建或当前权限校验错误
	}
	assigned := make(chan result, 1)
	username := "race-" + uuid.NewString()
	go func() {
		id, e := (AccountService{DB: second}).Create(ctx, audit, AccountCreateInput{Username: username, Password: "Concurrent!Password2026", RoleID: role})
		assigned <- result{id, e}
	}()
	select {
	case <-attempted:
	case <-ctx.Done():
		close(release)
		t.Fatal("second root lock not reached")
	}
	// 已确定第二次查询开始；短窗口只验证实际锁仍阻止提交，不以延时决定请求顺序。
	select {
	case got := <-assigned:
		close(release)
		cleanupManagementUser(t, first, got.id)
		t.Fatalf("assignment bypassed held role lock %v", got.err)
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	got := <-assigned
	cleanupManagementUser(t, first, got.id)
	if !errors.Is(got.err, ErrInsufficient) {
		t.Fatalf("revoked assignment admitted id=%d err=%v", got.id, got.err)
	}
	var count int64
	if err := first.Table("admin_user").Where("username=?", username).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("denied account count=%d %v", count, err)
	}
}
