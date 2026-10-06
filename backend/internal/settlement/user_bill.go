package settlement

import (
	"context"
	"errors"
	"fmt"
)

// ErrUserBillLookup 标识已读操作在所属账单读取阶段失败，HTTP沿用原404而非保存阶段503。
// 底层缺失、范围外、存储或取消原因通过errors.Is保留，不公开内部错误。
var ErrUserBillLookup = errors.New("user bill lookup failed")

// UserBillService 拥有所属账单的列表、详情及已读记录操作，不生成费用或重算资金。
type UserBillService struct {
	Repo BillRepo // 本用户账单与已读记录访问，连接由cmd管理。
}

// List 在ctx下按userID及原page／pageSize读取账单，返回数组、总数及查询／取消错误。
// HTTP已校验正整数分页；空页保留原空数组，来源金额只读，不执行账单或资金写入。
// 沿用原repo在已读补查失败时展示未读的规则，不新增该阶段的错误回执。
func (s UserBillService) List(ctx context.Context, userID uint64, page, pageSize int) ([]Bill, int64, error) {
	return s.Repo.ListBills(ctx, userID, page, pageSize)
}

// Detail 在ctx下按billID及userID同时限定归属，返回原账单或缺失／范围外／读取错误。
// 不根据其它用户账单暴露存在性，不保存已读记录。
func (s UserBillService) Detail(ctx context.Context, userID, billID uint64) (Bill, error) {
	return s.Repo.LoadByID(ctx, userID, billID)
}

// MarkRead 在ctx下先确认userID所属billID，再幂等保存原已读凭据，成功返回nil。
// 查询阶段失败包装ErrUserBillLookup且不写入；保存失败原样返回，调用方区分原404与503。
// 保持原两次操作顺序及连接事务边界，不新增资金写入或后台任务。
func (s UserBillService) MarkRead(ctx context.Context, userID, billID uint64) error {
	if _, err := s.Repo.LoadByID(ctx, userID, billID); err != nil {
		return fmt.Errorf("%w: %w", ErrUserBillLookup, err)
	}
	return s.Repo.MarkRead(ctx, userID, billID)
}
