package http

import (
	"gorm.io/gorm"
)

// AdminAccounts 是后台自有表（账号、角色、权限字典）的管理处理器。
// 权限判定一律走 Auth.Require，不在本层自行判断。
type AdminAccounts struct {
	DB   *gorm.DB // central_db 句柄
	Auth *AdminAuth
}
