// Package user 定义用户聚合的实体与值对象：登录身份与用户档案。
// 只有结构体与哨兵，不含 gorm、不含 SQL、不含 gin，
// 持久化与接口分别由 infrastructure 和 interface 承担。
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
	ID     uint64
	OpenID string
	IsNew  bool
}

// Profile 是用户档案的对外视图。UserID 以十进制字符串序列化，
// 避免前端把 Snowflake ID 当作 JavaScript Number 丢失精度。
type Profile struct {
	UserID       uint64    `json:"user_id,string"`
	Nickname     string    `json:"nickname"`
	AvatarURL    string    `json:"avatar_url"`
	PhoneBound   bool      `json:"phone_bound"`
	RegisteredAt time.Time `json:"registered_at"`
	Wallet       struct {
		AvailableCents int64 `json:"available_cents"`
		FrozenCents    int64 `json:"frozen_cents"`
	} `json:"wallet"`
	CouponUnusedCount int64 `json:"coupon_unused_count"`
}
