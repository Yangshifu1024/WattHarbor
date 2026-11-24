package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"wattharbor/internal/domain/operations"
)

// taskFileIdentity 检查服务生成的十六进制任务号，兼容早期短编号，新请求保留完整 UUID。
var taskFileIdentity = regexp.MustCompile(`^EXP[0-9A-F]{1,32}$`)

// artifactProof 保存导出文件的任务、范围、脱敏列和内容摘要，写入私有校验文件。
// 文件和 SQL 不能原子提交；只有 completed 且任务号、摘要、范围和脱敏规则均匹配时才可下载。
type artifactProof struct {
	Version       int                  `json:"version"`        // 私有格式版本，当前1
	TaskNo        string               `json:"task_no"`        // 完整任务号
	Resource      string               `json:"resource"`       // 资源声明
	Format        string               `json:"format"`         // 扩展名；私有校验文件保持 string，便于与历史文件逐字节比对
	RequestedBy   uint64               `json:"requested_by"`   // 申请人，不经浮点转换
	RowCount      uint32               `json:"row_count"`      // 本次实际行数
	Scope         operations.DataScope `json:"scope"`          // 对应资源实际执行的范围维度
	MaskedColumns []string             `json:"masked_columns"` // 本次文件已经遮蔽的公开列
	SHA256        string               `json:"sha256"`         // 内容文件的 SHA-256 摘要，下载时核对是否被修改
}

// effectiveScope 记录实际查询范围：订单和站点按站点，设备按站点与厂商交集，账单仅支持全局。
// 单独厂商限制不改变订单或站点查询，保存的范围只包含实际执行的筛选。
// ScopeType 与 ScopeID 主动写零值，保存的范围不携带单条范围类型；读取路径依赖该判定，
// 不得改成写入具体类型。
func effectiveScope(resource string, scope operations.DataScope) operations.DataScope {
	if resource != "devices" {
		scope.VendorIDs = nil
	}
	scope.Unrestricted = scope.Unrestricted || (len(scope.StationIDs) == 0 && len(scope.VendorIDs) == 0)
	scope.ScopeType = ""
	scope.ScopeID = 0
	return scope
}

// containsScope 检查当前范围是否覆盖文件保存时的全部范围；扩大权限可下载，收窄权限须拒绝旧文件。
func containsScope(current, saved operations.DataScope) bool {
	if current.Unrestricted {
		return true
	}
	if saved.Unrestricted {
		return false
	}
	covered := func(now, before []uint64) bool {
		if len(now) == 0 {
			return true
		}
		if len(before) == 0 {
			return false
		}
		for _, id := range before {
			if !slices.Contains(now, id) {
				return false
			}
		}
		return true
	}
	return covered(current.StationIDs, saved.StationIDs) && covered(current.VendorIDs, saved.VendorIDs)
}

// fileNames 按格式和合法任务号生成内容文件及校验文件名，不采用数据库中的任意路径。
// 历史 PDF 仅允许清理，下载只支持 CSV／XLSX。
// LegacyExportFormatPDF 不在 export_task.file_format 的 ENUM 约束内，
// 这里保留它只为识别早期遗留文件，并按其真实扩展名清理与拼接文件名。
func fileNames(row ExportTaskRow, cleanup bool) (string, string, error) {
	format := row.FileFormat
	if format == "" {
		format = ExportFormatCSV
	}
	if !taskFileIdentity.MatchString(row.TaskNo) {
		return "", "", &DownloadError{Message: "导出文件不可用，请重新导出"}
	}
	if !format.Known() && !(cleanup && format == LegacyExportFormatPDF) {
		return "", "", &DownloadError{Message: "导出格式不可用"}
	}
	return row.TaskNo + "." + string(format), row.TaskNo + ".scope.json", nil
}

// openRoot 打开配置目录，需要时先创建；调用方负责关闭受限文件句柄。
func (s ExportService) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.exportDir(), 0o700); err != nil {
			return nil, err
		}
	}
	return os.OpenRoot(s.exportDir())
}

// contextReader 在同步读取中响应取消，不负责关闭文件。
type contextReader struct {
	ctx   context.Context /* 请求取消 */
	input io.Reader       /* 读取句柄 */
}

// Read 响应取消后停止底层读取。
func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(p)
}

// publish 在导出目录生成内容和校验文件，先关闭 .partial 临时文件再改名发布。
// 调用方在任务锁事务中写完成状态；失败由同任务锁内的恢复步骤清理。
func publish(ctx context.Context, root *os.Root, row ExportTaskRow, spec ExportSpec, records []map[string]any, scope operations.DataScope, mask operations.FieldMask) (string, error) {
	name, proofName, err := fileNames(row, false)
	if err != nil {
		return "", err
	}
	// 新文件存于 v1 目录；缺少校验文件时必须拒绝下载，不能套用旧根目录文件的兼容规则。
	if err := root.MkdirAll("v1", 0o700); err != nil {
		return "", err
	}
	name, proofName = filepath.Join("v1", name), filepath.Join("v1", proofName)
	file, err := root.OpenFile(name+".partial", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	headers := make([]string, 0, len(spec.Columns))
	lines := make([][]string, 0, len(records))
	masked := []string{}
	for _, column := range spec.Columns {
		headers = append(headers, column.Header)
		if mask.Hide(spec.Table, column.Column) {
			masked = append(masked, column.Column)
		}
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return "", errors.Join(err, file.Close())
		}
		line := make([]string, 0, len(spec.Columns))
		for _, column := range spec.Columns {
			value := record[column.Column]
			if mask.Hide(spec.Table, column.Column) {
				value = operations.MaskPlaceholder
			}
			line = append(line, stringifyCell(value))
		}
		lines = append(lines, line)
	}
	format := row.FileFormat
	if format == "" {
		format = ExportFormatCSV
	}
	hash := sha256.New()
	err = (FormatWriter{}).Write(ctx, io.MultiWriter(file, hash), string(format), headers, lines)
	err = errors.Join(err, file.Close())
	if err != nil {
		return "", err
	}
	proof := artifactProof{Version: 1, TaskNo: row.TaskNo, Resource: row.Resource, Format: string(row.FileFormat), RequestedBy: row.RequestedBy, RowCount: uint32(len(records)), Scope: effectiveScope(row.Resource, scope), MaskedColumns: masked, SHA256: hex.EncodeToString(hash.Sum(nil))}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	if err := root.WriteFile(proofName+".partial", encoded, 0o600); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := root.Rename(name+".partial", name); err != nil {
		return "", err
	}
	if err := root.Rename(proofName+".partial", proofName); err != nil {
		return "", err
	}
	return name, nil
}

// stringifyCell 将 nil 转为空文本、字节转为文本，其余保留驱动数字和时间格式。
func stringifyCell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// removeArtifacts 在任务锁内按合法任务号删除内容、校验和临时文件，不采用数据库任意路径。
// 文件不存在视为已清理，不影响其他任务。
func removeArtifacts(root *os.Root, row ExportTaskRow) error {
	name, proof, err := fileNames(row, true)
	if err != nil {
		return err
	}
	var combined error
	// pending 回滚后即使没有 file_path，也可能已发布 v1 文件；清理须检查该任务号的两种布局。
	for _, path := range []string{name, proof, name + ".partial", proof + ".partial", filepath.Join("v1", name), filepath.Join("v1", proof), filepath.Join("v1", name+".partial"), filepath.Join("v1", proof+".partial")} {
		if err := root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			combined = errors.Join(combined, err)
		}
	}
	return combined
}

// verifyArtifact 打开文件并核对当前范围、脱敏规则和摘要，成功后将句柄移回开头。
// 校验文件不匹配即拒绝；旧根目录文件缺少校验文件时，仅全局且无需脱敏的账号可下载。
func verifyArtifact(ctx context.Context, root *os.Root, row ExportTaskRow, spec ExportSpec, scope operations.DataScope, mask operations.FieldMask, versioned bool) (file *os.File, err error) {
	name, proofName, err := fileNames(row, false)
	if err != nil {
		return nil, err
	}
	if row.FilePath == nil {
		return nil, &DownloadError{Message: "导出尚未完成"}
	}
	if versioned {
		name, proofName = filepath.Join("v1", name), filepath.Join("v1", proofName)
	}
	file, err = root.Open(name)
	if err != nil {
		return nil, err
	}
	opened := file
	defer func() {
		if err != nil {
			err = errors.Join(err, opened.Close())
			file = nil
		}
	}()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, &DownloadError{Message: "导出文件不可用，请重新导出"}
	}
	proofFile, err := root.Open(proofName)
	current := effectiveScope(row.Resource, scope)
	if errors.Is(err, os.ErrNotExist) {
		if versioned {
			return nil, &DownloadError{Message: "导出文件不可用，请重新导出"}
		}
		if !current.Unrestricted {
			return nil, &ForbiddenError{"旧导出缺少范围凭据，请重新导出"}
		}
		for _, column := range spec.Columns {
			if mask.Hide(spec.Table, column.Column) {
				return nil, &ForbiddenError{"当前脱敏规则已变化，请重新导出"}
			}
		}
		return file, nil
	}
	if err != nil {
		return nil, err
	}
	encoded, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, input: proofFile}, (64<<10)+1))
	err = errors.Join(readErr, proofFile.Close())
	if err != nil {
		return nil, err
	}
	var proof artifactProof
	if len(encoded) > 64<<10 || json.Unmarshal(encoded, &proof) != nil {
		return nil, &DownloadError{Message: "导出文件不可用，请重新导出"}
	}
	if proof.Version != 1 || proof.TaskNo != row.TaskNo || proof.Resource != row.Resource || proof.Format != string(row.FileFormat) || proof.RequestedBy != row.RequestedBy || proof.RowCount != row.RowCount {
		return nil, &DownloadError{Message: "导出文件不可用，请重新导出"}
	}
	if !containsScope(current, proof.Scope) {
		return nil, &ForbiddenError{"当前数据范围无权下载该导出"}
	}
	for _, column := range spec.Columns {
		if mask.Hide(spec.Table, column.Column) && !slices.Contains(proof.MaskedColumns, column.Column) {
			return nil, &ForbiddenError{"当前脱敏规则已变化，请重新导出"}
		}
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, input: file}); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != proof.SHA256 {
		return nil, &DownloadError{Message: "导出文件不可用，请重新导出"}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return file, nil
}

// expectedPath 按配置根目录和可信任务号生成路径，数据库路径仅用于比对。
func (s ExportService) expectedPath(row ExportTaskRow) (string, error) {
	name, _, err := fileNames(row, false)
	if row.FilePath != nil && *row.FilePath == filepath.Join(s.exportDir(), "v1", name) {
		name = filepath.Join("v1", name)
	}
	return filepath.Join(s.exportDir(), name), err
}
