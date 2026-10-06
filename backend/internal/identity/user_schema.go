package identity

import (
	"database/sql"
	"time"
)

// LoginIdentity 是 openid 唯一锁行，先提交后串行化用户及钱包开户，不承载会话。
type LoginIdentity struct {
	OpenID []byte `gorm:"column:openid;primaryKey"` // 原微信登录身份
}

// TableName 返回登录唯一身份所属表，无参数、错误或 IO。
func (LoginIdentity) TableName() string { return "user_login_identity" }

// UserAccount 仅映射用户准入所需列；钱包创建委托所属能力，号码绑定使用所属 repo。
type UserAccount struct {
	ID          uint64         `gorm:"column:id;primaryKey;autoIncrement:false"` // 原用户主键，显式 Snowflake，不使用数据库自增
	OpenID      string         `gorm:"column:openid"`                            // 原微信登录身份
	UnionID     sql.NullString `gorm:"column:unionid"`                           // 首次开放平台身份，可空
	Status      string         `gorm:"column:status"`                            // active 正常或 frozen 冻结
	LastLoginAt sql.NullTime   `gorm:"column:last_login_at"`                     // 最后登录 UTC 时刻，可空
}

// TableName 返回用户所属表，无参数、错误或 IO。
func (UserAccount) TableName() string { return "user" }

// profileRow 是公开档案的单条只读联表投影，不提供钱包或优惠券写入。
type profileRow struct {
	ID                uint64         // 原用户主键，显式 Snowflake，不使用数据库自增
	Nickname          sql.NullString // 原昵称，可空
	AvatarURL         sql.NullString // 原头像，可空
	Phone             sql.NullString // 原已绑定号码，可空
	FirstSeenAt       time.Time      // 注册 UTC 时刻
	BalanceCents      int64          // 原账面金额，分
	FrozenCents       int64          // 原预占金额，分
	CouponUnusedCount int64          // 原当前未过期权益数量
}
