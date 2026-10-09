package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// Generate 同步为申请人生成文件；按角色集合→账号→任务加锁，保持到文件、凭据发布和 completed 提交。
// 不启动后台任务，文件与 SQL 不保证原子提交。
func (s ExportService) Generate(ctx context.Context, taskNo string, original operations.Profile) error {
	if s.DB == nil {
		return errors.New("export unavailable")
	}
	var owned bool
	var mayHaveFiles bool // 本次已取得目录并进入发布，恢复不得吞目录清理失败
	err := s.generationTransaction(ctx, func(tx *gorm.DB) error {
		p, err := s.lockedActor(tx, original, "export.create")
		if err != nil {
			return err
		}
		row, err := s.repo().LockExportTaskByNo(tx, taskNo)
		if err != nil {
			return err
		}
		if row.RequestedBy != p.ID {
			return &ForbiddenError{"只能生成自己创建的导出文件"}
		}
		owned = true
		if row.Status == "completed" || row.Status == "failed" || row.Status == "expired" {
			return nil
		}
		if row.Status != "pending" && row.Status != "running" {
			return operations.ErrConflict
		}
		spec, known := ExportResource(row.Resource)
		if !known {
			return ErrUnknownExportResource
		}
		if !p.HasPermission(spec.Permission) {
			return &ForbiddenError{"没有导出该资源的权限"}
		}
		expiry, err := s.repo().Expiration(tx, row.ID)
		if err != nil {
			return err
		}
		if expiry != nil && !s.now().Before(*expiry) {
			return &DownloadError{Message: "导出文件已过期，请重新导出", Expired: true}
		}
		scope, err := ops.LockDataScopeInTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if row.Resource == "bills" && !scope.Unrestricted {
			return &ForbiddenError{"该财务资源只允许全局数据范围导出"}
		}
		mask, err := ops.LockFieldMaskInTx(ctx, tx, p.RoleID)
		if err != nil {
			return err
		}
		var filters map[string]any
		if row.FilterJSON != nil {
			if err := json.Unmarshal([]byte(*row.FilterJSON), &filters); err != nil {
				return err
			}
		}
		records, err := (ExportRepo{DB: tx}).QueryExportSource(ctx, row.Resource, scope, filters, s.maxRows()+1)
		if err != nil {
			return err
		}
		if len(records) > s.maxRows() {
			return fmt.Errorf("导出超过 %d 行，请缩小范围", s.maxRows())
		}
		root, err := s.openRoot(true)
		if err != nil {
			return err
		}
		mayHaveFiles = true
		name, publishErr := publish(ctx, root, row, spec, records, scope, mask)
		err = errors.Join(publishErr, root.Close())
		if err != nil {
			return err
		}
		// 取数和编码后须重新检查有效期，已发布字节不能作为过期任务完成的依据。
		now := s.now()
		if expiry != nil && !now.Before(*expiry) {
			return &DownloadError{Message: "导出文件已过期，请重新导出", Expired: true}
		}
		return s.repo().CompleteInTx(tx, taskNo, len(records), filepath.Join(s.exportDir(), name), now)
	})
	if err == nil || !owned {
		return err
	}
	// 取消后仅同步收尾，最多五秒，不继续取数或编码。
	// 先锁读当前状态；提交结果未知或另一请求已完成时保留文件，避免误删成功结果。
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	failed, settleErr := s.settleFailure(recovery, taskNo, original.ID, err, mayHaveFiles)
	if settleErr != nil {
		return errors.Join(err, settleErr)
	}
	if !failed {
		return nil
	}
	return &generationFailure{cause: err}
}

// settleFailure 锁读任务并核对申请人，只清理自己的未完成任务。
// 失败状态须确实保存才返回已失败；数据库不可用时保留 pending 供同申请恢复。
func (s ExportService) settleFailure(ctx context.Context, taskNo string, actorID uint64, cause error, mayHaveFiles bool) (bool, error) {
	failed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.repo().LockExportTaskByNo(tx, taskNo)
		if err != nil {
			return err
		}
		if row.RequestedBy != actorID {
			return &ForbiddenError{"只能生成自己创建的导出文件"}
		}
		if row.Status == "completed" || row.Status == "expired" || row.Status == "failed" {
			return nil
		}
		root, err := s.openRoot(false)
		if err != nil && !errors.Is(err, os.ErrNotExist) && mayHaveFiles {
			return err
		}
		if root != nil {
			err = errors.Join(removeArtifacts(root, row), root.Close())
			if err != nil {
				return err
			}
		}
		if err := s.repo().FailInTx(tx, taskNo, truncateReason(cause.Error(), 255)); err != nil {
			return err
		}
		failed = true
		return nil
	})
	return failed && err == nil, err
}

// truncateReason 按字节上限截断错误文本，不截断 UTF-8 字符。
func truncateReason(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
