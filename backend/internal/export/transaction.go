package export

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// generationTransaction 同步执行事务回调，提交或回滚由回调退出后手动完成。
// SQL 和文件仍响应请求取消，但取消不能提前释放保护文件的任务锁。
// 已有外层事务时用保存点，调用方须等回调退出才回滚外层事务。
func (s ExportService) generationTransaction(ctx context.Context, action func(*gorm.DB) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, nested := s.DB.Statement.ConnPool.(gorm.TxCommitter); nested {
		return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return errors.Join(action(tx), ctx.Err())
		})
	}
	// 等待连接响应请求取消；取得连接后手动结束事务，但不能在取消后继续查询或编码。
	return s.DB.WithContext(ctx).Connection(func(connection *gorm.DB) error {
		return connection.WithContext(context.WithoutCancel(ctx)).Transaction(func(tx *gorm.DB) error {
			return errors.Join(action(tx.WithContext(ctx)), ctx.Err())
		})
	})
}
