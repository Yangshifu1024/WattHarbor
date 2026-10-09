package settlement

import (
	"chargepilot/internal/order"
	"context"
	"gorm.io/gorm"
)

// QueryFeeRepo 向订单用例提供结算费用，排除数据库模型和 GORM 查询对象。
type QueryFeeRepo struct {
	DB *gorm.DB /* 所属结算连接或调用方事务 */
}

// Fees 批量读取并解码订单费用，缺失或非法回执不进入结果；空订单集合不查询。
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
