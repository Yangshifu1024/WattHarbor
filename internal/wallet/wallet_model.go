package wallet

// WalletAccount 是身份开户所需的钱包持久化最小模型，金额使用数据库默认初始值。
type WalletAccount struct {
	ID     uint64 `gorm:"column:id;primaryKey"` // 钱包主键
	UserID uint64 `gorm:"column:user_id"`       // 用户身份，一用户一个账户
}

// TableName 返回钱包账户所属表，不执行查询或写入。
func (WalletAccount) TableName() string { return "wallet_account" }
