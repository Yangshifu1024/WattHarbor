package card

import (
	"chargepilot/internal/domain/money"
	"context"
	"errors"
	"gorm.io/gorm"
)

// ErrCardStatusInput 表示用户状态或卡号输入不满足业务约束。
var ErrCardStatusInput = errors.New("card status input invalid")

// CardStatusService 提供用户所属卡的查询、挂失和解绑及设备余额读取。
type CardStatusService struct {
	DB *gorm.DB // central 卡与钱包连接，由进程管理
}

// List 返回 userID 自己尚未解绑的卡；结果空集合为 []，不读取他人卡。
func (s CardStatusService) List(ctx context.Context, userID uint64) ([]OnlineCard, error) {
	return (UserCardRepo{DB: s.DB}).List(ctx, userID)
}

// Change 将 userID 所属 cardNo 的状态改为 status；仅允许 lost/unbound。
// 返回输入、缺卡；状态和用户审计同事务，失败不保留半次变更，不结束现有充电。
func (s CardStatusService) Change(ctx context.Context, userID uint64, cardNo, status string) error {
	if status != "lost" && status != "unbound" {
		return ErrCardStatusInput
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := UserCardRepo{DB: tx}
		card, err := repo.LockUserCard(tx, cardNo, userID)
		if err != nil {
			return err
		}
		return repo.SetUserCardStatus(tx, card, userID, status)
	})
}

// CardBalance 返回设备有效标志及可用余额，单位分；无效卡不泄露绑定身份。
type CardBalance struct {
	Valid        bool  `json:"valid"`         // 卡、用户及钱包均启用且存在
	BalanceCents int64 `json:"balance_cents"` // 扣除冻结额度后的可用金额，下界零
}

// Balance 校验 cardNo 并读取共享钱包余额，返回结果或输入、数据库错误。
// 缺失或非活跃卡返回 valid=false、金额零；不把余额写在实体卡上，不扣款。
func (s CardStatusService) Balance(ctx context.Context, cardNo string) (CardBalance, error) {
	if !money.CanonicalCardNumber(cardNo) {
		return CardBalance{}, ErrCardStatusInput
	}
	cents, err := CardAvailableCents(ctx, s.DB, cardNo)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CardBalance{}, nil
	}
	if err != nil {
		return CardBalance{}, err
	}
	return CardBalance{Valid: true, BalanceCents: cents}, nil
}
