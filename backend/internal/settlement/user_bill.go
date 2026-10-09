package settlement

import (
	"context"
	"errors"
	"fmt"
)

// ErrUserBillLookup 标识账单归属查询失败，HTTP 映射为 404；保存失败仍映射为 503。
// errors.Is 保留底层错误，响应不暴露内部原因。
var ErrUserBillLookup = errors.New("user bill lookup failed")

// UserBillService 拥有所属账单的列表、详情及已读记录操作，不生成费用或重算资金。
type UserBillService struct {
	Repo BillRepo // 本用户账单与已读记录访问，连接由 cmd 管理。
}

// List 分页读取本人账单；已读状态补查失败时仍按未读展示。
func (s UserBillService) List(ctx context.Context, userID uint64, page, pageSize int) ([]Bill, int64, error) {
	return s.Repo.ListBills(ctx, userID, page, pageSize)
}

// Detail 按用户和账单 ID 同时限定归属，避免暴露他人账单是否存在。
func (s UserBillService) Detail(ctx context.Context, userID, billID uint64) (Bill, error) {
	return s.Repo.LoadByID(ctx, userID, billID)
}

// MarkRead 先核对本人账单，再幂等保存已读记录。
// 查询失败包装 ErrUserBillLookup，保存失败原样返回，供 HTTP 区分 404 和 503。
func (s UserBillService) MarkRead(ctx context.Context, userID, billID uint64) error {
	if _, err := s.Repo.LoadByID(ctx, userID, billID); err != nil {
		return fmt.Errorf("%w: %w", ErrUserBillLookup, err)
	}
	return s.Repo.MarkRead(ctx, userID, billID)
}
