package identity

import (
	"chargepilot/internal/wallet"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"chargepilot/internal/platform/snowflake"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserRepo 持有进程管理的 central 连接；用户与开户事务由对应用例负责。
type UserRepo struct {
	DB *gorm.DB /* 进程管理的用户连接或调用方事务 */
}

// Login 按 openID 恢复或创建用户。
// MySQL 死锁或锁等待最多重试五次；已有用户更新登录时间，新用户与零余额钱包同事务创建。
func (s UserRepo) Login(ctx context.Context, openID, unionID string) (User, error) {
	if openID == "" || len(openID) > 64 || len(unionID) > 64 {
		return User{}, errors.New("invalid WeChat identity")
	}
	for attempt := range 5 {
		user, err := s.loginOnce(ctx, openID, unionID)
		if !retryableMySQL(err) {
			return user, err
		}
		select {
		case <-ctx.Done():
			return User{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 20 * time.Millisecond):
		}
	}
	return User{}, errors.New("concurrent login retries exhausted")
}

// retryableMySQL 只允许重试 MySQL 1213 死锁与 1205 锁等待错误。
func retryableMySQL(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

// loginOnce 先以自动提交创建 openID 唯一编号，再在事务中恢复用户或创建用户及钱包。
func (s UserRepo) loginOnce(ctx context.Context, openID, unionID string) (User, error) {
	// 身份行先自动提交，避免事务中 INSERT IGNORE 在取得行锁前因缺失键间隙锁发生死锁。
	if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&LoginIdentity{OpenID: []byte(openID)}).Error; err != nil {
		return User{}, err
	}
	var user User
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity LoginIdentity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("openid = ?", []byte(openID)).Take(&identity).Error; err != nil {
			return err
		}
		var current UserAccount
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("openid = ? AND deleted_at IS NULL", openID).Order("id ASC").Take(&current).Error
		if err == nil {
			if current.Status != "active" {
				return ErrUserFrozen
			}
			now := time.Now().UTC()
			if err := tx.Model(&UserAccount{}).Where("id = ?", current.ID).Update("last_login_at", now).Error; err != nil {
				return err
			}
			user = User{ID: current.ID, OpenID: openID}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		id, err := snowflake.Next(tx)
		if err != nil {
			return err
		}
		created := UserAccount{ID: id, OpenID: openID, Status: "active", LastLoginAt: sql.NullTime{Time: now, Valid: true}}
		if unionID != "" {
			created.UnionID = sql.NullString{String: unionID, Valid: true}
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if err := (wallet.UserWalletRepo{}).OpenWalletAccount(tx, created.ID); err != nil {
			return err
		}
		user = User{ID: created.ID, OpenID: openID, IsNew: true}
		return nil
	})
	return user, err
}

// Active 读取 id 未删除用户是否 active，返回检查结果；无用户为 false，不修改状态。
func (s UserRepo) Active(ctx context.Context, id uint64) (bool, error) {
	var user UserAccount
	err := s.DB.WithContext(ctx).Select("status").Where("id = ? AND deleted_at IS NULL", id).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("user status: %w", err)
	}
	return user.Status == "active", nil
}

// LockStatusInTx 锁定未删除用户并读取当前状态，供同事务操作检查。
// 调用方须先锁用户，再锁模板或钱包。
func LockStatusInTx(tx *gorm.DB, id uint64) (string, error) {
	var user struct {
		Status string `gorm:"column:status"` /* 账户状态 */
	}
	err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&user).Error
	return user.Status, err
}

// Profile 联表读取启用用户的档案、钱包与权益，只读，不创建余额或优惠券。
func (s UserRepo) Profile(ctx context.Context, id uint64) (Profile, error) {
	var row profileRow
	result := s.DB.WithContext(ctx).Raw(`SELECT u.id, u.nickname, u.avatar_url, u.phone, u.first_seen_at,
		w.balance_cents, w.frozen_cents,
		(SELECT COUNT(*) FROM coupon_grant AS g WHERE g.user_id = u.id AND g.status = 'unused' AND g.expired_at > NOW(3) AND g.deleted_at IS NULL) AS coupon_unused_count
		FROM user AS u JOIN wallet_account AS w ON w.user_id = u.id AND w.deleted_at IS NULL
		WHERE u.id = ? AND u.status = 'active' AND u.deleted_at IS NULL LIMIT 1`, id).Scan(&row)
	if result.Error != nil {
		return Profile{}, result.Error
	}
	if result.RowsAffected != 1 {
		return Profile{}, gorm.ErrRecordNotFound
	}
	profile := Profile{UserID: row.ID, Nickname: row.Nickname.String, AvatarURL: row.AvatarURL.String, PhoneBound: row.Phone.Valid && row.Phone.String != "", RegisteredAt: row.FirstSeenAt.UTC(), CouponUnusedCount: row.CouponUnusedCount}
	profile.Wallet.AvailableCents = row.BalanceCents
	profile.Wallet.FrozenCents = row.FrozenCents
	return profile, nil
}

// ErrPhoneTaken 表示手机号已被其他账号占用，用于将唯一键冲突转换为明确业务错误。
var ErrPhoneTaken = errors.New("手机号已被占用")

// BindPhone 在同一事务内检查并写入手机号，唯一索引防止并发绑定同号。
// value 为 nil 时写入 SQL NULL 以解绑，不能用空字符串替代。
func (s UserRepo) BindPhone(ctx context.Context, userID uint64, value *string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var holder int64
		if value != nil {
			if err := tx.Table("user").Where("phone = ? AND id <> ? AND deleted_at IS NULL", *value, userID).Count(&holder).Error; err != nil {
				return err
			}
			if holder > 0 {
				return ErrPhoneTaken
			}
		}
		var bound any
		if value != nil {
			bound = *value
		}
		return tx.Table("user").Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"phone": bound}).Error
	})
}
