package identity

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"gorm.io/gorm"
)

// UserRechargeHistory 由钱包提供后台用户充值页，身份模块不映射或更新支付表。
type UserRechargeHistory interface {
	// History 在 ctx 下返回 userID 的 page 充值记录或读取错误；用户存在性由本用例核对。
	History(ctx context.Context, userID uint64, page persist.PageQuery) (persist.Page[wallet.AdminRechargeRow], error)
}

// UserQueryService 拥有后台用户范围、分页及档案组合，保留全局 user.read 权限。
type UserQueryService struct {
	DB        *gorm.DB            // 进程管理的用户与跨领域只读投影连接
	Recharges UserRechargeHistory // cmd 注入所属钱包读取能力
}

// List 在 ctx 下以 actor 当前 user.read 权限按 q 分页，批量补齐公开事实，返回原页或错误。
// 不逐用户外部调用，不冻结账号或改写金额；分页及派生字段沿用原语义。
func (s UserQueryService) List(ctx context.Context, actor operations.Profile, q persist.PageQuery) (persist.Page[ChargeUserRow], error) {
	if !actor.HasPermission("user.read") {
		return persist.Page[ChargeUserRow]{}, operations.ErrForbidden
	}
	rows, total, err := ChargeUsers(ctx, s.DB, q)
	if err != nil {
		return persist.Page[ChargeUserRow]{}, err
	}
	if err := DecorateChargeUsers(ctx, s.DB, rows); err != nil {
		return persist.Page[ChargeUserRow]{}, err
	}
	return persist.Page[ChargeUserRow]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize, Permissions: actor.Permissions}, nil
}

// Detail 在 ctx 下以 actor 权限读取 id 用户的既有档案，返回批量补齐结果或缺失／读取错误。
// 最近订单沿用原20条上限，钱包／优惠／报障都是只读投影，不隐含站点范围。
func (s UserQueryService) Detail(ctx context.Context, actor operations.Profile, id uint64) (ChargeUserDetail, error) {
	if !actor.HasPermission("user.read") {
		return ChargeUserDetail{}, operations.ErrForbidden
	}
	out, err := LoadChargeUserDetail(ctx, s.DB, id, ChargeUserDetailOrderLimit)
	if err != nil {
		return out, err
	}
	rows := []ChargeUserRow{out.ChargeUserRow}
	if err := DecorateChargeUsers(ctx, s.DB, rows); err != nil {
		return ChargeUserDetail{}, err
	}
	out.ChargeUserRow = rows[0]
	return out, nil
}

// RechargeHistory 在 ctx 下按 actor 权限核对 id 未删除用户，再读取 q 原充值页；返回缺失／读取错误。
// 已冻结用户历史仍可运营读取，不将冻结误当删除，金额读取委托钱包所属窄能力。
func (s UserQueryService) RechargeHistory(ctx context.Context, actor operations.Profile, id uint64, q persist.PageQuery) (persist.Page[wallet.AdminRechargeRow], error) {
	if !actor.HasPermission("user.read") {
		return persist.Page[wallet.AdminRechargeRow]{}, operations.ErrForbidden
	}
	if err := RequireUserExists(ctx, s.DB, id); err != nil {
		return persist.Page[wallet.AdminRechargeRow]{}, err
	}
	return s.Recharges.History(ctx, id, q)
}
