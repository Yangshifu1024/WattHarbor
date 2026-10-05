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

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"gorm.io/gorm"
)

// Generate 在ctx下为taskNo原申请人original同步生成，返回取消、准入、查询、IO或状态错误。
// 集合／账号→任务锁持续到文件及范围凭据发布、完成状态提交；不启动后台任务，不宣称SQL／文件原子。
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
		// 取数及编码可能跨过下载截止；发布字节不代表允许把过期产物标成completed。
		now := s.now()
		if expiry != nil && !now.Before(*expiry) {
			return &DownloadError{Message: "导出文件已过期，请重新导出", Expired: true}
		}
		return s.repo().CompleteInTx(tx, taskNo, len(records), filepath.Join(s.exportDir(), name), now)
	})
	if err == nil || !owned {
		return err
	}
	// 只在受管理调用内同步收尾，最多5秒；不继续取数／编码。原取消ctx不能落失败状态。
	// 先当前重读，提交回执未知或另一请求已完成时保留已完成文件，不盲删竞争者成果。
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

// settleFailure 在ctx下当前锁读taskNo并核对actorID，仅为自己未完成任务清理和记录cause。
// 返回是否持久failed及恢复错误；SQL不可用时不宣称已失败，原pending可由同申请重新生成。
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

// truncateReason 按原字节上限max保留value且不切断UTF-8，返回可落库错误文本，无IO。
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
