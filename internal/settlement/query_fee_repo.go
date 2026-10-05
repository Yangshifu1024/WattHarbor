package settlement

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"gorm.io/gorm"
)

// QueryFeeRepo 向订单读取能力提供所属结算回执，不把模型或 GORM builder 暴露给订单用例。
type QueryFeeRepo struct {
	DB *gorm.DB /* 所属结算连接或调用方事务 */
}

// Fees 在 ctx 下用一次查询读取 ids 原回执，返回成功解码金额映射或读取错误，无资金写入。
// 缺失及无法解码的回执不进入映射，保持原历史未确认费用为空的语义；空 ids 不查询。
func (s QueryFeeRepo) Fees(ctx context.Context, ids []uint64) (map[uint64]order.FeeFacts, error) {
	out := make(map[uint64]order.FeeFacts, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []ChargeFeeRecord
	if err := s.DB.WithContext(ctx).Where("charge_order_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		electric, service, total, ok := row.Fees()
		if ok {
			out[row.ChargeOrderID] = order.FeeFacts{OrderID: row.ChargeOrderID, ElectricCents: electric, ServiceCents: service, TotalCents: total, ShortfallCents: row.ShortfallCents}
		}
	}
	return out, nil
}
