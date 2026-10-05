package ops

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"strings"
	"time"
)

// mfaEnrollmentTTL 保留五分钟待绑定窗口，确认还须核对绝对到期时刻。
const mfaEnrollmentTTL = 5 * time.Minute

var (
	ErrMFAProof        = errors.New("密码或验证码校验失败")            // 当前密码或TOTP证明失败
	ErrMFAPending      = errors.New("绑定已过期或已被替换，请重新开始")      // 原绑定凭据已失效
	ErrMFAState        = errors.New("双因素认证状态已改变，请刷新后重试")     // 当前状态不支持原动作
	ErrMFABusy         = errors.New("账号安全设置正在处理中，请稍后重试")     // 同账号设置租锁已占用
	ErrMFAInput        = errors.New("请按操作提供当前密码、绑定标识和六位验证码") // 输入证明组合不合要求
	ErrMFASettingsRate = errors.New("安全验证过于频繁，请稍后重试")        // 一分钟安全设置超过五次
)

// MFASettingsInput 仅接收动作与当前证明，不允许客户端指定目标账号或密钥。
type MFASettingsInput struct {
	Action       string `json:"action"`        // enrol／confirm／disable原动作
	Password     string `json:"password"`      // 登记或关闭时的当前口令
	Code         string `json:"code"`          // 确认或关闭时的六位TOTP
	EnrollmentID string `json:"enrollment_id"` // 确认时的原随机绑定标识
}

// MFASettingsReply 保留原登记材料及状态字段，密钥仅在口令通过的新登记响应返回。
type MFASettingsReply struct {
	EnrollmentID    string `json:"enrollment_id,omitempty"` // 原确认标识
	Secret          string `json:"secret,omitempty"`        // 服务端新密钥，不写入审计
	OTPAuthURI      string `json:"otpauth_uri,omitempty"`   // 当前账号的验证器绑定URI
	ExpiresIn       int    `json:"expires_in,omitempty"`    // 绑定寿命，秒
	MFAEnabled      bool   `json:"mfa_enabled"`             // 当前操作后的启用事实
	SessionsRevoked bool   `json:"sessions_revoked"`        // 是否已提升认证版本使旧会话失效
}

// Valid 核对输入动作和各证明字段的组合，返回准入事实，无IO；六位码仅允许数字。
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

// MFAService 拥有当前账号安全设置规则、租锁和SQL状态／审计事务。
type MFAService struct {
	DB   *gorm.DB // 所属账号数据库
	Repo MFARepo  // 所属缓存IO
}

// Apply 在ctx下为profile原会话sid执行input并用ip审计，返回原设置回执或输入／证明／冲突／存储错误。
// 同账号设置串行，十秒操作期限短于三十秒租期；同步延迟清理仅释放自己的租锁。
// Redis原凭据消费先于SQL提交，后续SQL失败不恢复凭据，必须重新登记；不宣称跨存储原子提交。
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

// apply 在ctx下锁p原账号并核对sid绑定凭据及in证明，ip用于审计；返回回执或回滚错误。
// 密码／坏码不改变安全状态；登记仅写短期缓存，确认／关闭提升认证版本并同事务审计。
func (s MFAService) apply(ctx context.Context, p operations.Profile, sid string, in MFASettingsInput, ip string) (MFASettingsReply, error) {
	var result MFASettingsReply
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account, err := (AdminStore{DB: tx}).LockActiveAdmin(tx, p.ID)
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
			if err := auth.VerifyTOTP(pending.Secret, in.Code, time.Now().UTC()); err != nil {
				return ErrMFAProof
			}
			// 在更新前原子消费相同缓存。坏码不消费，过期或并发替换不启用。
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
			if err := auth.VerifyTOTP(*account.MFASecret, in.Code, time.Now().UTC()); err != nil {
				return ErrMFAProof
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
