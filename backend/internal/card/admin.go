package card

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	"gorm.io/gorm"
)

// ErrCardAdminInput 标识绑定或运营状态输入不满足原业务约束。
var ErrCardAdminInput = errors.New("card admin input invalid")

// CardBindInput 描述人工核验后的绑定请求；主键由 HTTP 保留十进制精度后转换。
type CardBindInput struct {
	CardNo string // 规范1–19位非零十进制绑定卡号，前后空白按原行为去除
	UserID uint64 // 已启用且未删除的目标用户
	Reason string // 人工核验依据，非空且最多500字
}

// CardChangeInput 描述人工卡状态及核验依据。
type CardChangeInput struct {
	Status string `json:"status"` // active/lost/disabled/unbound；解绑状态不得直接改回其他状态
	Reason string `json:"reason"` // 原人工依据，非空且最多500字
}

// CardAdminService 拥有绑定、启停及卡审计事务；卡绑定用户而不绑定站点。
type CardAdminService struct {
	DB *gorm.DB // central 卡连接，由进程管理
}

// List 在 ctx 下按 actor 的 user.read 权限返回最新500张原卡；不隐式套用站点范围。
// 返回列表或权限／存储错误；运营在线卡是全局用户管理能力，没有设备或站点归属列。
func (s CardAdminService) List(ctx context.Context, actor operations.Profile) ([]OnlineCard, error) {
	if !actor.HasPermission("user.read") {
		return nil, operations.ErrForbidden
	}
	return (CardAdminRepo{DB: s.DB}).List(ctx)
}

// Bind 在 ctx 下以 actor 的实时 online_card.manage 权限绑定 in，返回原卡主键或输入／冲突／存储错误。
// 先校验用户，再以卡号唯一键争用首次写入，随后行锁核对现有归属；同人已启用重放不再审计。
// 新建或重新绑定与核验审计同事务；他人绑定拒绝，失败全部回滚，不替用户开户或迁移余额。
func (s CardAdminService) Bind(ctx context.Context, actor operations.Profile, in CardBindInput) (uint64, error) {
	if !actor.HasPermission("online_card.manage") {
		return 0, operations.ErrForbidden
	}
	in.CardNo = strings.TrimSpace(in.CardNo)
	if !money.CanonicalCardNumber(in.CardNo) || in.UserID == 0 || !operations.ValidText(strings.TrimSpace(in.Reason), 500) {
		return 0, ErrCardAdminInput
	}
	var id uint64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identity.RequireActiveUserInTx(tx, in.UserID); err != nil {
			return err
		}
		repo := CardAdminRepo{DB: tx}
		created, err := repo.ClaimBindingInTx(tx, in.CardNo, in.UserID)
		if err != nil {
			return err
		}
		card, err := repo.LockCardByNo(tx, in.CardNo)
		if err != nil {
			return err
		}
		id = card.ID
		if !created {
			if card.Status != "unbound" {
				if card.UserID == in.UserID && card.Status == "active" {
					return nil
				}
				return fmt.Errorf("%w: %s", operations.ErrConflict, ErrCardAlreadyBound)
			}
			if err := repo.BindOnlineCard(tx, id, in.UserID); err != nil {
				return err
			}
		}
		return repo.AppendOnlineCardAudit(tx, OnlineCardAuditInput{CardID: id, ActorID: actor.ID, Action: "bind", Detail: in.Reason})
	})
	return id, err
}

// ChangeStatus 在 ctx 下以 actor 的实时管理权限将 id 卡改为 in.Status，返回准入或存储错误。
// 已解绑卡只能保留解绑；卡行锁、状态与人工依据审计同事务，不改变现有订单或共享钱包。
func (s CardAdminService) ChangeStatus(ctx context.Context, actor operations.Profile, id uint64, in CardChangeInput) error {
	if !actor.HasPermission("online_card.manage") {
		return operations.ErrForbidden
	}
	if (in.Status != "active" && in.Status != "lost" && in.Status != "disabled" && in.Status != "unbound") || !operations.ValidText(in.Reason, 500) {
		return ErrCardAdminInput
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := CardAdminRepo{DB: tx}
		row, err := repo.LockCardByID(tx, id)
		if err != nil {
			return err
		}
		if row.Status == "unbound" && in.Status != "unbound" {
			return operations.ErrConflict
		}
		if err := repo.SetOnlineCardStatus(tx, row, in.Status); err != nil {
			return err
		}
		return repo.AppendOnlineCardAudit(tx, OnlineCardAuditInput{CardID: id, ActorID: actor.ID, Action: in.Status, Detail: in.Reason})
	})
}
