package account

import (
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/ops"
)

// AdminAccounts 是后台账号／角色HTTP适配，只持有所属服务与鉴权适配。
type AdminAccounts struct {
	Accounts ops.AccountService  // 账号管理及目标MFA完整用例
	Roles    ops.RoleService     // 角色和权限字典完整用例
	Auth     *authhttp.AdminAuth // 请求鉴权与原上下文
}
