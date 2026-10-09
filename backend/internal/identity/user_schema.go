package identity

import (
	"database/sql"
	"time"
)

// LoginIdentity 是 openid 唯一锁行，先提交后串行化用户及钱包开户，不承载会话。
type LoginIdentity struct {
	OpenID []byte `gorm:"column:openid;primaryKey"` // 微信登录身份
}

// TableName 返回登录唯一编号所属表。
func (LoginIdentity) TableName() string { return "user_login_identity" }

// UserAccount 仅映射用户状态校验所需列；钱包创建委托对应模块的接口，号码绑定使用对应仓储。
type UserAccount struct {
	ID          uint64         `gorm:"column:id;primaryKey;autoIncrement:false"` // 用户主键，显式 Snowflake，不使用数据库自增
	OpenID      string         `gorm:"column:openid"`                            // 微信登录身份
	UnionID     sql.NullString `gorm:"column:unionid"`                           // 首次开放平台身份，可空
	Status      string         `gorm:"column:status"`                            // active 正常或 frozen 冻结
	LastLoginAt sql.NullTime   `gorm:"column:last_login_at"`                     // 最后登录 UTC 时刻，可空
}

// TableName 返回用户所属表。
func (UserAccount) TableName() string { return "user" }

// profileRow 是公开档案的单条只读联表查询结果，不提供钱包或优惠券写入。
type profileRow struct {
	ID                uint64         // 用户主键，显式 Snowflake，不使用数据库自增
	Nickname          sql.NullString // 昵称，可空
	AvatarURL         sql.NullString // 头像，可空
	Phone             sql.NullString // 已绑定号码，可空
	FirstSeenAt       time.Time      // 注册 UTC 时刻
	BalanceCents      int64          // 账面金额，分
	FrozenCents       int64          // 预占金额，分
	CouponUnusedCount int64          // 当前未过期权益数量
}
