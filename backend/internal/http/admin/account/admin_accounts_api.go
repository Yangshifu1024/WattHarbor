// Package account 提供后台账号、角色和权限管理接口。
package account

import (
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/ops"
)

// AdminAccounts 是后台账号／角色 HTTP 适配，只持有服务与鉴权适配。
type AdminAccounts struct {
	Accounts ops.AccountService  // 账号管理及目标 MFA 完整用例
	Roles    ops.RoleService     // 角色和权限字典完整用例
	Auth     *authhttp.AdminAuth // 请求鉴权与上下文
}
