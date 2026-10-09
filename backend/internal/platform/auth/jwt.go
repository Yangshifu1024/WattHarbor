// Package auth 提供 JWT 签名校验、动态口令和随机密钥。
package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrSecretTooShort 表示 JWT 签名密钥少于 32 字节。
	ErrSecretTooShort = errors.New("JWT secret must be at least 32 bytes")
	// ErrInvalidToken 表示 JWT 无效、过期或身份类型不匹配。
	ErrInvalidToken = errors.New("invalid or expired JWT")
)

// Claims 保存令牌中的身份和有效期；IssuedAt、ExpiresAt 为 Unix 秒数。
// RoleIDs 是签发时的角色记录，权限检查仍须读取当前角色。
type Claims struct {
	Subject   string
	Kind      string // user or admin
	OpenID    string
	SessionID string
	RoleIDs   []uint64
	IssuedAt  int64
	ExpiresAt int64
}

type wireClaims struct {
	Kind      string   `json:"kind"`
	OpenID    string   `json:"openid,omitempty"`
	SessionID string   `json:"sid,omitempty"`
	RoleIDs   []uint64 `json:"role_ids,omitempty"`
	jwt.RegisteredClaims
}

// JWT 使用共享密钥签发和校验 HS256 令牌。
type JWT struct {
	key []byte
}

// NewJWT 创建 HS256 签名器；密钥至少为 32 字节。
func NewJWT(secret string) (*JWT, error) {
	if len(secret) < 32 {
		return nil, ErrSecretTooShort
	}
	return &JWT{key: []byte(secret)}, nil
}

// Sign 校验身份字段后签发令牌，过期时间必须晚于签发时间。
func (j *JWT) Sign(claims Claims) (string, error) {
	if !validClaims(claims) || claims.ExpiresAt <= claims.IssuedAt {
		return "", ErrInvalidToken
	}
	wire := wireClaims{
		Kind:      claims.Kind,
		OpenID:    claims.OpenID,
		SessionID: claims.SessionID,
		RoleIDs:   claims.RoleIDs,
		Subject:   claims.Subject,
		IssuedAt:  jwt.NewNumericDate(time.Unix(claims.IssuedAt, 0)),
		ExpiresAt: jwt.NewNumericDate(time.Unix(claims.ExpiresAt, 0)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, wire).SignedString(j.key)
}

// Verify 按 now 校验签名、有效期和身份字段，身份类型必须与 expectedKind 一致。
// 此处只校验令牌，不查询会话、账号状态或当前权限。
func (j *JWT) Verify(token, expectedKind string, now time.Time) (Claims, error) {
	wire := &wireClaims{}
	parsed, err := jwt.ParseWithClaims(token, wire, func(_ *jwt.Token) (any, error) {
		return j.key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || !parsed.Valid || wire.IssuedAt == nil || wire.ExpiresAt == nil {
		return Claims{}, ErrInvalidToken
	}
	claims := Claims{
		Subject:   wire.Subject,
		Kind:      wire.Kind,
		OpenID:    wire.OpenID,
		SessionID: wire.SessionID,
		RoleIDs:   wire.RoleIDs,
		IssuedAt:  wire.IssuedAt.Unix(),
		ExpiresAt: wire.ExpiresAt.Unix(),
	}
	if !validClaims(claims) || claims.Kind != expectedKind || claims.IssuedAt > now.Unix()+60 {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func validClaims(c Claims) bool {
	if c.Kind != "user" && c.Kind != "admin" || c.Subject == "" || c.IssuedAt <= 0 || c.ExpiresAt <= 0 {
		return false
	}
	if _, err := strconv.ParseUint(c.Subject, 10, 64); err != nil {
		return false
	}
	if c.Kind == "user" && c.OpenID == "" {
		return false
	}
	if c.Kind == "admin" && c.OpenID != "" {
		return false
	}
	return true
}
