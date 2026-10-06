package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"gorm.io/gorm"
)

// Download 是准入通过后打开的原文件；HTTP只消费句柄，结束时负责关闭，不再读取数据库路径。
type Download struct {
	File       *os.File  // 任务锁内已打开、校验、回到开头的内容句柄
	Name       string    // 可信任务号与扩展名推导的下载名称
	MIME       string    // 原CSV或XLSX内容类型
	ModifiedAt time.Time // 原文件修改时刻，供HTTP条件／范围请求
}

// GetDownload 在ctx下核对original当前权限、id归属／范围／脱敏及锁后到期，返回句柄或公开准入／状态／IO错误。
// 下载与清理共用任务锁，锁内打开句柄；退出事务后HTTP消费，不在传输期间占用数据库锁。
func (s ExportService) GetDownload(ctx context.Context, original operations.Profile, id uint64) (out Download, err error) {
	if s.DB == nil {
		return out, errors.New("export unavailable")
	}
	expired := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := s.lockedActor(tx, original, "export.create")
		if err != nil {
			return err
		}
		row, err := s.repo().LockExportTaskByID(tx, id)
		if err != nil {
			return err
		}
		if row.RequestedBy != p.ID && !p.HasPermission("admin_user.read") {
			return &ForbiddenError{"只能下载自己创建的导出文件"}
		}
		spec, known := ExportResource(row.Resource)
		if !known || !p.HasPermission(spec.Permission) {
			return &ForbiddenError{"没有下载该导出的权限"}
		}
		scope, err := ops.LockDataScopeInTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if row.Resource == "bills" && !scope.Unrestricted {
			return &ForbiddenError{"当前数据范围无权下载该财务导出"}
		}
		if row.Status == "expired" {
			expired = true
			return nil
		}
		if row.Status != "completed" || row.FilePath == nil {
			return &DownloadError{Message: "导出尚未完成"}
		}
		expiry, err := s.repo().Expiration(tx, row.ID)
		if err != nil {
			return err
		}
		if expiry != nil && !s.now().Before(*expiry) {
			if err := s.expireInTx(ctx, tx, row); err != nil {
				return err
			}
			expired = true
			return nil
		}
		expected, err := s.expectedPath(row)
		if err != nil {
			return err
		}
		if *row.FilePath != expected {
			return &DownloadError{Message: "导出文件不可用，请重新导出"}
		}
		mask, err := ops.LockFieldMaskInTx(ctx, tx, p.RoleID)
		if err != nil {
			return err
		}
		root, err := s.openRoot(false)
		if err != nil {
			return err
		}
		base, _, err := fileNames(row, false)
		if err != nil {
			return errors.Join(err, root.Close())
		}
		versioned := expected == filepath.Join(s.exportDir(), "v1", base)
		file, openErr := verifyArtifact(ctx, root, row, spec, scope, mask, versioned)
		err = errors.Join(openErr, root.Close())
		if err != nil {
			if file != nil {
				err = errors.Join(err, file.Close())
			}
			return err
		}
		stat, err := file.Stat()
		if err != nil {
			return errors.Join(err, file.Close())
		}
		// 摘要校验也可能跨过截止；关闭句柄后在同任务锁内清理，避免Windows占用文件。
		if expiry != nil && !s.now().Before(*expiry) {
			if err := file.Close(); err != nil {
				return err
			}
			if err := s.expireInTx(ctx, tx, row); err != nil {
				return err
			}
			expired = true
			return nil
		}
		name, _, err := fileNames(row, false)
		if err != nil {
			return errors.Join(err, file.Close())
		}
		mime := "text/csv; charset=utf-8"
		if row.FileFormat == "xlsx" {
			mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		}
		out = Download{File: file, Name: name, MIME: mime, ModifiedAt: stat.ModTime()}
		return nil
	})
	if err != nil {
		if out.File != nil {
			err = errors.Join(err, out.File.Close())
			out = Download{}
		}
		return out, err
	}
	if expired {
		return Download{}, &DownloadError{Message: "导出文件已过期，请重新导出", Expired: true}
	}
	return out, nil
}

// expireInTx 在ctx及tx已持row任务锁下，仅从可信根推导并清理旧文件，再标记expired，返回IO／存储错误。
// 不使用数据库FilePath删除；异常任务身份拒绝删除，不存在的文件视为已清理。
func (s ExportService) expireInTx(ctx context.Context, tx *gorm.DB, row ExportTaskRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.openRoot(false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if root != nil {
		err = errors.Join(removeArtifacts(root, row), root.Close())
		if err != nil {
			return err
		}
	}
	return s.repo().ExpireExportTask(tx, row.TaskNo)
}

// CleanupExpired 在ctx下取至多1000原completed／failed候选并逐条当前锁读，返回推进数或取消／IO／存储错误。
// 每条重新核对时刻和状态，重复清理不重复计数；不接管生成任务，不启动新goroutine。
func (s ExportService) CleanupExpired(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, errors.New("export unavailable")
	}
	now := s.now()
	rows, err := s.repo().ListExpirableExportTasks(s.DB.WithContext(ctx), now, 1000)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, candidate := range rows {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		changed := false
		err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			row, err := s.repo().LockExportTaskByID(tx, candidate.ID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if row.Status != "completed" && row.Status != "failed" {
				return nil
			}
			expiry, err := s.repo().Expiration(tx, row.ID)
			if err != nil {
				return err
			}
			if expiry == nil || s.now().Before(*expiry) {
				return nil
			}
			if err := s.expireInTx(ctx, tx, row); err != nil {
				return err
			}
			changed = true
			return nil
		})
		if err != nil {
			return removed, err
		}
		if changed {
			removed++
		}
	}
	return removed, nil
}
