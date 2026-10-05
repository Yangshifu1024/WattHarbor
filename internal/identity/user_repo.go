package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/snowflake"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserRepo 持有进程管理的 central 连接；用户与开户事务由所属用例拥有。
type UserRepo struct {
	DB *gorm.DB /* 进程管理的用户连接或调用方事务 */
}

// Login 在 ctx 下按 openID/unionID 恢复或创建用户，返回身份或输入、冻结、存储错误。
// 死锁／锁等待仅按原有界五次策略重试；已有用户只更新登录时间，新用户与所属钱包开户同事务。
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

// retryableMySQL 核对 err 是否为1213死锁或1205锁等待，返回可重试事实，无 IO。
func retryableMySQL(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

// loginOnce 在 ctx 下以 openID 唯一身份串行准入，并记录 unionID 首次事实。
// 返回恢复／新建身份或错误；身份行先自动提交，用户和零余额开户在后续同事务中提交。
func (s UserRepo) loginOnce(ctx context.Context, openID, unionID string) (User, error) {
	// 以自动提交模式创建 identity 行。
	// 事务里并发的 INSERT IGNORE
	// 可能在 FOR UPDATE 之前就因缺失键间隙而死锁。
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

// Active 在 ctx 下读取 id 未删除用户是否 active，返回事实或读取错误；无用户为 false，不修改状态。
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

// LockStatusInTx 在 tx 原context下以 id 取得未删除用户写锁并返回原status或缺失／存储错误。
// 供同事务业务准入使用；调用方必须在模板或钱包锁前取得用户锁，不自行提交或修改身份。
func LockStatusInTx(tx *gorm.DB, id uint64) (string, error) {
	var user struct {
		Status string `gorm:"column:status"` /* 原账户状态 */
	}
	err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&user).Error
	return user.Status, err
}

// Profile 在 ctx 下读取 id 活跃用户的单次联表档案，返回身份、钱包和权益投影或缺失／存储错误。
// 金额沿用原公开语义，不扣款；跨领域只读投影不创建资金或优惠券。
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

// ErrPhoneTaken 表示手机号已被其他账号占用。号码归属由唯一索引最终兜底，
// 本哨兵用于把并发冲突映射成明确的业务错误而非通用 500。
var ErrPhoneTaken = errors.New("手机号已被占用")

// BindPhone 写入或清除 user 表的手机号。号码归属检查与写入在同一事务内完成：
// 先查占用以给出明确提示，唯一索引负责并发兜底。
// phone 传 nil 表示解绑。解绑必须显式传 nil 而不是空字符串——GORM 的 Updates
// 会忽略字符串零值，用 nil 判断并直接写 SQL NULL 才是解绑的正确语义。
// ctx 控制 userID 号码事务，value 非空表示原规范号码；返回归属或数据库错误，调用方校验来源。
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
