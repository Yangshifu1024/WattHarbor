package ops

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
)

// ManagementInputError 保存管理输入拒绝的公开文案，不包含凭据或存储细节。
type ManagementInputError struct {
	Message string /* 原公开输入提示 */
}

// Error 返回e原输入提示，无IO。
func (e *ManagementInputError) Error() string { return e.Message }

// ManagementConflict 保存当前管理状态拒绝文案，可按领域冲突统一匹配。
type ManagementConflict struct {
	Message string /* 当前状态拒绝原因 */
}

// Error 返回e原状态提示，无IO。
func (e *ManagementConflict) Error() string { return e.Message }

// Unwrap 返回领域冲突以供HTTP错误匹配，无副作用。
func (e *ManagementConflict) Unwrap() error { return operations.ErrConflict }

var (
	ErrInsufficient          = errors.New("不能授予自己未拥有的权限")                   // 原角色授予越权拒绝
	ErrLastAdmin             = &ManagementConflict{"必须保留至少一个有效的客户管理员账号"}    // 禁止移除最后有效客户管理员
	ErrSelfDelete            = &ManagementConflict{"不能删除当前登录账号"}            // 原本人删除拒绝
	ErrSelfMFA               = &ManagementConflict{"请使用本人账号的安全设置修改双因素认证"}   // 本人只走个人安全设置
	ErrBuiltinRoleDelete     = &ManagementConflict{"内置角色不可删除：它承载运营自身的访问权限"} // 内置角色不删除
	ErrOwnRoleDelete         = &ManagementConflict{"不能删除自己正在使用的角色"}         // 原当前角色删除拒绝
	ErrRoleInUse             = &ManagementConflict{"该角色下仍有账号，请先改派后再删除"}     // 未删除账号仍引用角色
	ErrManagementRoleMissing = errors.New("角色不存在")                          // 原角色端点特有404文案
)

// readManagementActor 在ctx下重读original原账号／版本及permission，返回当前档案或凭据／权限／存储错误。
// 只读列表不根据传入档案的旧权限授权，不写入会话或数据范围。
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

// lockedManagementActor 在tx下锁角色集合→original原账号→其当前角色及权限，返回实时档案或准入错误。
// 所有账号分配／角色集合写入共用客户管理员角色行作为串行点；锁后当前读不信任旧RR或HTTP菜单。
// 调用方须将实际变更及审计留在同一事务，禁止先锁目标账号再回头申请集合锁。
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

// assignManagementRole 在tx下核对roleID当前角色及全部权限不超过actor，返回空角色／越权／存储错误。
// create决定原空角色为输入400还是状态409；角色锁与权限当前读在集合串行窗口内完成。
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
