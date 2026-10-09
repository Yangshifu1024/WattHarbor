package identity

import "gorm.io/gorm"

// RequireActiveUserInTx 确认卡绑定用户未删除且启用；不符合时返回错误。
// 沿用调用方事务，不创建账户或修改卡与钱包。
func RequireActiveUserInTx(tx *gorm.DB, userID uint64) error {
	var row struct {
		Status string // 用户启用状态
	}
	return tx.Table("user").Where("id=? AND status='active' AND deleted_at IS NULL", userID).Take(&row).Error
}
