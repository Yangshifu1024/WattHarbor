package card

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"wattharbor/internal/domain/money"
)

// ErrCardStatusInput 表示用户状态或卡号输入不满足业务约束。
var ErrCardStatusInput = errors.New("card status input invalid")

// CardStatus 是 online_card.status 列的卡片当前状态，供列表、挂失解绑与运营启停共用。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （active/lost/disabled/unbound）。
//
// 命名陷阱：domain/card 另有 CardStatusConfirming/CardStatusConfirmed/CardStatusFailed，
// 那是 card_operation.status 三值词表（同为 status 列，但不同表、不同词表），
// 两者不得互相赋值或比较。本类型描述「一张实体卡当前可用性」，
// CardOperationStatus 描述「一次刷卡操作的执行结论」。
type CardStatus string

const (
	// CardActive 表示卡已绑定且可用。
	CardActive CardStatus = "active"
	// CardLost 表示用户挂失，不可再用于刷卡。
	CardLost CardStatus = "lost"
	// CardDisabled 表示运营停用，不可再用于刷卡。
	CardDisabled CardStatus = "disabled"
	// CardUnbound 表示已解绑，不再关联用户。
	CardUnbound CardStatus = "unbound"
)

// Known 判断是否属于该列的数据库合法集合（四值）。
// 仅用于识别未知值；读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s CardStatus) Known() bool {
	switch s {
	case CardActive, CardLost, CardDisabled, CardUnbound:
		return true
	default:
		return false
	}
}

// UserWritable 判断是否属于 C 端自助入口允许写入的子集。
// 用户只能挂失或解绑自己的卡，不能自行启用或停用（那两个仅运营可写）。
// 该集合是用户操作准入规则，不是列的合法取值集合，不得用作单一收口。
func (s CardStatus) UserWritable() bool {
	return s == CardLost || s == CardUnbound
}

// ManageWritable 判断是否属于运营后台状态变更允许写入的子集（四值全开）。
// 解绑后的卡仍不可改回其他状态，该约束由 ChangeStatus 的单独判定保证，不在此收口。
func (s CardStatus) ManageWritable() bool {
	return s.Known()
}

// AllUserWritableStatuses 是 C 端可写子集的取值串，供入参白名单直接引用。
const AllUserWritableStatuses = "lost unbound"

// AllCardStatuses 是该列的全量取值串，仅用于展示与读模型枚举；
// 不得用作写入准入白名单。
const AllCardStatuses = "active lost disabled unbound"

// CardStatusService 提供用户所属卡的查询、挂失和解绑及设备余额读取。
type CardStatusService struct {
	DB *gorm.DB // central 卡与钱包连接，由进程管理
}

// List 返回 userID 自己尚未解绑的卡；结果空集合为 []，不读取他人卡。
func (s CardStatusService) List(ctx context.Context, userID uint64) ([]OnlineCard, error) {
	return (UserCardRepo{DB: s.DB}).List(ctx, userID)
}

// Change 将 userID 所属 cardNo 的状态改为 status；仅允许 lost/unbound（CardStatus 的 C 端可写子集）。
// 返回输入、缺卡；状态和用户审计同事务，失败不保留半次变更，不结束现有充电。
func (s CardStatusService) Change(ctx context.Context, userID uint64, cardNo string, status CardStatus) error {
	if !status.UserWritable() {
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
