package identity

import (
	"context"
	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/persist"
	"wattharbor/internal/wallet"
)

// UserRechargeHistory 由钱包提供后台用户充值页，身份模块不映射或更新支付表。
type UserRechargeHistory interface {
	// History 返回 userID 的 page 充值记录；用户存在性由本用例核对。
	History(ctx context.Context, userID uint64, page persist.PageQuery) (persist.Page[wallet.AdminRechargeRow], error)
}

// UserQueryService 负责后台用户范围、分页及档案组合，保留全局 user.read 权限。
type UserQueryService struct {
	DB        *gorm.DB            // 进程管理的用户与跨领域只读只读查询连接
	Recharges UserRechargeHistory // cmd 注入所属钱包读取能力
}

// List 校验 actor 当前 user.read 权限，按 q 分页并批量填入钱包和订单统计。
// 不逐用户外部调用，不冻结账号或改写金额；分页及派生字段沿用既有含义。
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

// Detail 按实时 user.read 权限读取用户档案，不按站点过滤。
// 最近订单最多 20 条，钱包、优惠券与报障只读取已有记录。
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

// RechargeHistory 按 actor 权限核对 id 未删除用户，再读取 q 充值页；返回缺失、读取错误。
// 已冻结用户历史仍可运营读取，不将冻结误当删除，金额读取委托钱包对应模块的接口。
func (s UserQueryService) RechargeHistory(ctx context.Context, actor operations.Profile, id uint64, q persist.PageQuery) (persist.Page[wallet.AdminRechargeRow], error) {
	if !actor.HasPermission("user.read") {
		return persist.Page[wallet.AdminRechargeRow]{}, operations.ErrForbidden
	}
	if err := RequireUserExists(ctx, s.DB, id); err != nil {
		return persist.Page[wallet.AdminRechargeRow]{}, err
	}
	return s.Recharges.History(ctx, id, q)
}
