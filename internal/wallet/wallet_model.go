package wallet

// 钱包账户的表模型。原先与用户登录、资料读写混在 user_repository.go 里，
// 那个文件是按调用方命名的，不是按它管的表。

type WalletAccount struct {
	ID     uint64 `gorm:"column:id;primaryKey"`
	UserID uint64 `gorm:"column:user_id"`
}

func (WalletAccount) TableName() string { return "wallet_account" }
