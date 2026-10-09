package ops

import (
	"context"
	"errors"

	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
)

// ManagementInputError 保存管理输入拒绝的公开文案，不包含凭据或存储细节。
type ManagementInputError struct {
	Message string /* 公开输入提示 */
}

// Error 返回输入提示。
func (e *ManagementInputError) Error() string { return e.Message }

// ManagementConflict 保存当前管理状态拒绝文案，可按领域冲突统一匹配。
type ManagementConflict struct {
	Message string /* 当前状态拒绝因 */
}

// Error 返回状态提示。
func (e *ManagementConflict) Error() string { return e.Message }

// Unwrap 返回领域冲突以供 HTTP 错误匹配，无副作用。
func (e *ManagementConflict) Unwrap() error { return operations.ErrConflict }

var (
	ErrInsufficient          = errors.New("不能授予自己未拥有的权限")                   // 角色授予越权拒绝
	ErrLastAdmin             = &ManagementConflict{"必须保留至少一个有效的客户管理员账号"}    // 禁止移除最后有效客户管理员
	ErrSelfDelete            = &ManagementConflict{"不能删除当前登录账号"}            // 本人删除拒绝
	ErrSelfMFA               = &ManagementConflict{"请使用本人账号的安全设置修改双因素认证"}   // 本人只走个人安全设置
	ErrBuiltinRoleDelete     = &ManagementConflict{"内置角色不可删除：它承载运营自身的访问权限"} // 内置角色不删除
	ErrOwnRoleDelete         = &ManagementConflict{"不能删除自己正在使用的角色"}         // 当前角色删除拒绝
	ErrRoleInUse             = &ManagementConflict{"该角色下仍有账号，请先改派后再删除"}     // 未删除账号仍引用角色
	ErrManagementRoleMissing = errors.New("角色不存在")                          // 角色端点特有404文案
)

// readManagementActor 重新核对账号版本和当前权限，不使用传入资料的旧权限授权。
func readManagementActor(ctx context.Context, db *gorm.DB, original operations.Profile, permission string) (operations.Profile, error) {
	if db == nil {
		return operations.Profile{}, errors.New("ops management unavailable")
	}
	current, err := (AccountRepo{DB: db}).Profile(ctx, original.ID)
	if err != nil {
		return operations.Profile{}, err
	}
	if current.AuthVersion != original.AuthVersion {
		return operations.Profile{}, operations.ErrCredentials
	}
	if !current.HasPermission(permission) {
		return operations.Profile{}, ErrPermission
	}
	return current, nil
}

// lockedManagementActor 按客户管理员角色→操作者账号→当前角色及权限加锁核对。
// 账号分配和角色变更共用该角色锁；须先取得集合锁，再锁目标账号。
// 权限以锁后的当前读为准，业务变更和审计留在同一事务。
func lockedManagementActor(tx *gorm.DB, original operations.Profile, permission string) (operations.Profile, error) {
	store := AccountRepo{DB: tx}
	if err := store.LockCustomerAdminRole(tx); err != nil {
		return operations.Profile{}, err
	}
	account, err := store.LockActiveAdmin(tx, original.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return operations.Profile{}, operations.ErrCredentials
	}
	if err != nil {
		return operations.Profile{}, err
	}
	if account.AuthVersion != original.AuthVersion {
		return operations.Profile{}, operations.ErrCredentials
	}
	role, err := (RoleRepo{DB: tx}).LockRole(tx, account.RoleID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return operations.Profile{}, operations.ErrCredentials
	}
	if err != nil {
		return operations.Profile{}, err
	}
	codes, err := (RoleRepo{DB: tx}).RolePermissionCodesInTx(tx, account.RoleID)
	if err != nil {
		return operations.Profile{}, err
	}
	current := operations.Profile{AuthVersion: account.AuthVersion, ID: account.ID, Username: account.Username, DisplayName: account.DisplayName.String, RoleID: account.RoleID, Role: role.Code, RoleName: role.Name, MFAEnabled: account.MFAEnabled, Permissions: codes}
	if !current.HasPermission(permission) {
		return operations.Profile{}, ErrPermission
	}
	return current, nil
}

// assignManagementRole 在角色锁内读取当前权限，禁止授予操作者没有的权限。
// 角色不存在时，新建返回输入错误，更新返回状态冲突。
func assignManagementRole(tx *gorm.DB, actor operations.Profile, roleID uint64, create bool) error {
	role, err := (RoleRepo{DB: tx}).LockRole(tx, roleID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var codes []string
	if err == nil {
		codes, err = (RoleRepo{DB: tx}).RolePermissionCodesInTx(tx, role.ID)
		if err != nil {
			return err
		}
	}
	if len(codes) == 0 {
		if create {
			return &ManagementInputError{"角色不存在或没有任何权限"}
		}
		return operations.ErrConflict
	}
	for _, code := range codes {
		if !actor.HasPermission(code) {
			return ErrInsufficient
		}
	}
	return nil
}
