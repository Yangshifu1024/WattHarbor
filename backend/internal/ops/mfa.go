package ops

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/auth"
	"context"
	"crypto/subtle"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"strings"
	"time"
)

// mfaEnrollmentTTL 限制待绑定窗口为五分钟，确认时仍核对截止时间。
const mfaEnrollmentTTL = 5 * time.Minute

var (
	ErrMFAProof        = errors.New("密码或验证码校验失败")            // 当前密码或 TOTP 验证码校验失败
	ErrMFAPending      = errors.New("绑定已过期或已被替换，请重新开始")      // 待绑定记录已过期或被替换
	ErrMFAState        = errors.New("双因素认证状态已改变，请刷新后重试")     // 当前状态不支持动作
	ErrMFABusy         = errors.New("账号安全设置正在处理中，请稍后重试")     // 同账号设置租锁已占用
	ErrMFAInput        = errors.New("请按操作提供当前密码、绑定标识和六位验证码") // 动作所需的密码、验证码或绑定号缺失或不合法
	ErrMFASettingsRate = errors.New("安全验证过于频繁，请稍后重试")        // 一分钟安全设置超过五次
)

// MFASettingsInput 接收设置动作、当前密码、验证码和绑定号，不允许指定目标账号或密钥。
type MFASettingsInput struct {
	Action       string `json:"action"`        // enrol／confirm／disable 动作
	Password     string `json:"password"`      // 登记或关闭时的当前口令
	Code         string `json:"code"`          // 确认或关闭时的六位 TOTP
	EnrollmentID string `json:"enrollment_id"` // 确认时的随机绑定标识
}

// MFASettingsReply 返回登记材料和状态，密钥只在密码验证通过的新登记响应出现。
type MFASettingsReply struct {
	EnrollmentID    string `json:"enrollment_id,omitempty"` // 确认标识
	Secret          string `json:"secret,omitempty"`        // 服务端新密钥，不写入审计
	OTPAuthURI      string `json:"otpauth_uri,omitempty"`   // 当前账号的验证器绑定 URI
	ExpiresIn       int    `json:"expires_in,omitempty"`    // 绑定寿命，秒
	MFAEnabled      bool   `json:"mfa_enabled"`             // 当前操作后是否启用 MFA
	SessionsRevoked bool   `json:"sessions_revoked"`        // 是否已提升认证版本使旧会话失效
}

// Valid 检查各 MFA 动作必填及禁止提交的字段；六位验证码只能包含数字。
func (in MFASettingsInput) Valid() bool {
	password := in.Password != "" && len(in.Password) <= 72
	code := len(in.Code) == 6
	for _, c := range in.Code {
		if c < '0' || c > '9' {
			code = false
		}
	}
	switch in.Action {
	case "enrol":
		return password && in.Code == "" && in.EnrollmentID == ""
	case "confirm":
		return in.Password == "" && code && len(in.EnrollmentID) == 43
	case "disable":
		return password && code && in.EnrollmentID == ""
	default:
		return false
	}
}

// MFAService 负责当前账号 MFA 设置、租锁和 SQL 状态及审计事务。
type MFAService struct {
	DB   *gorm.DB // 所属账号数据库
	Repo MFARepo  // 所属缓存 IO
}

// Apply 串行处理同一账号 MFA 设置，操作限十秒，短于三十秒锁租期，只释放自己的锁。
// Redis 挑战在 SQL 提交前删除，之后 SQL 失败不能恢复挑战，须重新登记。
func (s MFAService) Apply(ctx context.Context, profile operations.Profile, sid string, input MFASettingsInput, ip string) (MFASettingsReply, error) {
	input.Code = strings.TrimSpace(input.Code)
	if !input.Valid() {
		return MFASettingsReply{}, ErrMFAInput
	}
	if sid == "" {
		return MFASettingsReply{}, operations.ErrCredentials
	}
	if s.DB == nil || s.Repo.Redis == nil {
		return MFASettingsReply{}, errors.New("account security unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := s.Repo.Count(ctx, profile.ID)
	if err != nil {
		return MFASettingsReply{}, err
	}
	if n > 5 {
		return MFASettingsReply{}, ErrMFASettingsRate
	}
	owner, err := auth.RandomSecret(16)
	if err != nil {
		return MFASettingsReply{}, err
	}
	acquired, err := s.Repo.Acquire(ctx, profile.ID, owner, 30*time.Second)
	if err != nil {
		return MFASettingsReply{}, err
	}
	if !acquired {
		return MFASettingsReply{}, ErrMFABusy
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = s.Repo.Release(cleanup, profile.ID, owner)
	}()
	return s.apply(ctx, profile, sid, input, ip)
}

// apply 锁定账号，核对会话绑定的挑战和证明后执行设置，IP 用于审计。
// 坏密码或坏码不改安全状态；登记只写短期缓存，确认和关闭提升版本并保存审计。
func (s MFAService) apply(ctx context.Context, p operations.Profile, sid string, in MFASettingsInput, ip string) (MFASettingsReply, error) {
	var result MFASettingsReply
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account, err := (AccountRepo{DB: tx}).LockActiveAdmin(tx, p.ID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return operations.ErrCredentials
			}
			return err
		}
		if account.AuthVersion != p.AuthVersion {
			return operations.ErrCredentials
		}
		switch in.Action {
		case "enrol":
			if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(in.Password)) != nil {
				return ErrMFAProof
			}
			if account.MFAEnabled {
				return ErrMFAState
			}
			secret, err := auth.NewTOTPSecret()
			if err != nil {
				return err
			}
			id, err := auth.RandomSecret(32)
			if err != nil {
				return err
			}
			uri, err := auth.TOTPURI("ChargePilot", account.Username, secret)
			if err != nil {
				return err
			}
			pending := mfaEnrollment{ID: id, Secret: secret, AdminID: account.ID, AuthVersion: account.AuthVersion, SessionID: sid, ExpiresAt: time.Now().UTC().Add(mfaEnrollmentTTL)}
			if err := s.Repo.SavePending(ctx, account.ID, pending, mfaEnrollmentTTL); err != nil {
				return err
			}
			// 登记只写短期缓存，不改变当前 MFA，也不撤销会话。
			result = MFASettingsReply{EnrollmentID: id, Secret: secret, OTPAuthURI: uri, ExpiresIn: int(mfaEnrollmentTTL / time.Second)}
			return nil
		case "confirm":
			if account.MFAEnabled {
				return ErrMFAState
			}
			pending, raw, err := s.Repo.Pending(ctx, account.ID)
			if err != nil {
				return err
			}
			if pending.AdminID != account.ID || pending.AuthVersion != account.AuthVersion || pending.SessionID != sid || !time.Now().UTC().Before(pending.ExpiresAt) || subtle.ConstantTimeCompare([]byte(pending.ID), []byte(in.EnrollmentID)) != 1 {
				return ErrMFAPending
			}
			step, err := auth.VerifyTOTP(pending.Secret, in.Code, time.Now().UTC())
			if err != nil {
				return ErrMFAProof
			}
			// 更新前比较并原子删除读取过的挑战；坏码、过期或已替换挑战不能启用 MFA。
			consumed, err := s.Repo.ConsumePending(ctx, account.ID, raw)
			if err != nil {
				return err
			}
			if !consumed {
				return ErrMFAPending
			}
			if err := UpdateAdminMFASecret(tx, account.ID, &pending.Secret, true, true); err != nil {
				return err
			}
			// 首次确认消费新密钥时间步；重新登录须等待验证器切换验证码。
			updated, err := (AccountRepo{DB: tx}).LockActiveAdmin(tx, account.ID)
			if err != nil {
				return err
			}
			if err := consumeAdminTOTPStep(tx, updated, step); err != nil {
				return err
			}
			// audit 只记录账号和动作，永不持久化密钥、验证码或密码。
			if err := WriteAuthAudit(tx, account, "mfa_enable", ip); err != nil {
				return err
			}
			result = MFASettingsReply{MFAEnabled: true, SessionsRevoked: true}
			return nil
		case "disable":
			if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(in.Password)) != nil {
				return ErrMFAProof
			}
			if !account.MFAEnabled || account.MFASecret == nil {
				return ErrMFAState
			}
			step, err := auth.VerifyTOTP(*account.MFASecret, in.Code, time.Now().UTC())
			if err != nil {
				return ErrMFAProof
			}
			if err := consumeAdminTOTPStep(tx, account, step); err != nil {
				return err
			}
			if err := s.Repo.ClearPending(ctx, account.ID); err != nil {
				return err
			}
			if err := UpdateAdminMFASecret(tx, account.ID, nil, false, true); err != nil {
				return err
			}
			if err := WriteAuthAudit(tx, account, "mfa_disable", ip); err != nil {
				return err
			}
			result = MFASettingsReply{SessionsRevoked: true}
			return nil
		default:
			return ErrMFAState
		}
	})
	return result, err
}
