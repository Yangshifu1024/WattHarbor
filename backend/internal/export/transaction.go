package export

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// generationTransaction 在ctx下取得连接并执行action，返回取消、事务或action错误，不启动后台任务。
// Begin的生命周期仅由同步回调提交／回滚；SQL及文件操作仍用原ctx，取消不得使database/sql提前释放文件锁。
// 已有外层事务时采用保存点，外层调用者须保证事务直到同步回调退出才回滚；cmd注入普通池。
func (s ExportService) generationTransaction(ctx context.Context, action func(*gorm.DB) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, nested := s.DB.Statement.ConnPool.(gorm.TxCommitter); nested {
		return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return errors.Join(action(tx), ctx.Err())
		})
	}
	// 连接等待响应原ctx；只为已取得连接的Begin保留手动结束权，不继续取消后的取数或编码。
	return s.DB.WithContext(ctx).Connection(func(connection *gorm.DB) error {
		return connection.WithContext(context.WithoutCancel(ctx)).Transaction(func(tx *gorm.DB) error {
			return errors.Join(action(tx.WithContext(ctx)), ctx.Err())
		})
	})
}
