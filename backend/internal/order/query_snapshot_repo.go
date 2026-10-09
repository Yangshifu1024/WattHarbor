package order

import (
	"context"
	"gorm.io/gorm"
)

// SnapshotRepo 提供订单所属冻结计价数据的批量读取，不泄露可继续拼接的查询。
type SnapshotRepo struct {
	DB *gorm.DB // 所属订单连接或调用方只读事务，由进程管理
}

// Snapshots 按 ids 订单批量读取冻结 JSON，返回 ID 映射。
// 缺失订单不进入映射，空 ids 不查询；不以当前站点方案补充缺失的计价规则。
func (r SnapshotRepo) Snapshots(ctx context.Context, ids []uint64) (map[uint64][]byte, error) {
	out := make(map[uint64][]byte, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := ListPricingSnapshots(ctx, r.DB, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ChargeOrderID] = row.PricingSnapshot
	}
	return out, nil
}
