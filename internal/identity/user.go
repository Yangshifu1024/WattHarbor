// Package identity 拥有用户准入、会话、手机号及档案用例；表访问集中在所属 repo。
package identity

import (
	"errors"
	"time"
)

// ErrUserFrozen 表示账号已被冻结，不允许继续使用会话。
var ErrUserFrozen = errors.New("user account frozen")

// User 是登录身份的最小事实集合。IsNew 表示本次登录是否刚创建账号，
// 供接口层决定是否下发新用户引导。
type User struct {
	ID     uint64 // 原用户主键，跨服务传递时使用十进制字符串
	OpenID string // 原微信或独立开发命名空间身份
	IsNew  bool   // 本次登录事务是否首次开户
}

// Profile 是用户档案的对外视图。UserID 以十进制字符串序列化，
// 避免前端把 Snowflake ID 当作 JavaScript Number 丢失精度。
type Profile struct {
	UserID       uint64    `json:"user_id,string"` // 精确用户身份
	Nickname     string    `json:"nickname"`       // 原公开昵称，未设置为空
	AvatarURL    string    `json:"avatar_url"`     // 原头像地址，未设置为空
	PhoneBound   bool      `json:"phone_bound"`    // 是否存在已绑定号码
	RegisteredAt time.Time `json:"registered_at"`  // 首次注册 UTC 时间
	Wallet       struct {
		AvailableCents int64 `json:"available_cents"` // 钱包可用余额，分
		FrozenCents    int64 `json:"frozen_cents"`    // 钱包冻结余额，分
	} `json:"wallet"` // 所属钱包只读投影
	CouponUnusedCount int64 `json:"coupon_unused_count"` // 未删除且未过期的未用券数量
}
