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

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
)

// taskFileIdentity 限定服务生成的十六进制任务号；兼容早期较短身份，但新申请完整保留UUID。
var taskFileIdentity = regexp.MustCompile(`^EXP[0-9A-F]{1,32}$`)

// artifactProof 是服务端私有发布凭据，不写公开filter_json或新增数据库列。
// 文件、SQL不跨存储原子；只有completed且身份、摘要、范围及脱敏证明都通过才能提供新文件。
type artifactProof struct {
	Version       int                  `json:"version"`        // 私有格式版本，当前1
	TaskNo        string               `json:"task_no"`        // 原完整任务身份
	Resource      string               `json:"resource"`       // 原资源声明
	Format        string               `json:"format"`         // 原扩展名
	RequestedBy   uint64               `json:"requested_by"`   // 原申请人，不经浮点转换
	RowCount      uint32               `json:"row_count"`      // 本次实际行数
	Scope         operations.DataScope `json:"scope"`          // 对应资源实际执行的范围维度
	MaskedColumns []string             `json:"masked_columns"` // 本次文件已经遮蔽的公开列
	SHA256        string               `json:"sha256"`         // 生成字节摘要，防止文件与凭据失配
}

// effectiveScope 保留resource原读取维度：订单／站点按站点，设备为站点与厂商交集，账单仅全局。
// 仅厂商配置不自动改变订单／站点接口语义；证明记录实际执行范围，不虚称未过滤的厂商已受限。
func effectiveScope(resource string, scope operations.DataScope) operations.DataScope {
	if resource != "devices" {
		scope.VendorIDs = nil
	}
	scope.Unrestricted = scope.Unrestricted || (len(scope.StationIDs) == 0 && len(scope.VendorIDs) == 0)
	scope.ScopeType = ""
	scope.ScopeID = 0
	return scope
}

// containsScope 判断current实际读取范围是否覆盖saved所有维度；扩权允许，收窄不放行旧宽文件。
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

// fileNames 按row已登记格式和服务任务身份推导相对路径，返回内容及凭据名或状态错误。
// cleanup允许历史pdf清理，但提供下载只允许csv／xlsx；任何数据库任意路径都不成为文件操作参数。
func fileNames(row ExportTaskRow, cleanup bool) (string, string, error) {
	format := row.FileFormat
	if format == "" {
		format = "csv"
	}
	if !taskFileIdentity.MatchString(row.TaskNo) {
		return "", "", &DownloadError{Message: "导出文件不可用，请重新导出"}
	}
	if format != "csv" && format != "xlsx" && !(cleanup && format == "pdf") {
		return "", "", &DownloadError{Message: "导出格式不可用"}
	}
	return row.TaskNo + "." + format, row.TaskNo + ".scope.json", nil
}

// openRoot 打开s可信目录，create时先创建目录；返回受限文件句柄或IO错误，调用方负责关闭。
func (s ExportService) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.exportDir(), 0o700); err != nil {
			return nil, err
		}
	}
	return os.OpenRoot(s.exportDir())
}

// contextReader 在原同步读取中响应取消，不取得文件关闭权。
type contextReader struct {
	ctx   context.Context /* 请求取消 */
	input io.Reader       /* 原读取句柄 */
}

// Read 从input读取p并返回字节数／错误；取消后不继续底层读取。
func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(p)
}

// publish 在ctx及root下将row记录编码为内容和私有proof，返回文件名或取消／IO错误。
// .partial先关闭再发布，数据库状态随后由任务锁事务推进；失败清理由原任务锁内的恢复步骤执行。
func publish(ctx context.Context, root *os.Root, row ExportTaskRow, spec ExportSpec, records []map[string]any, scope operations.DataScope, mask operations.FieldMask) (string, error) {
	name, proofName, err := fileNames(row, false)
	if err != nil {
		return "", err
	}
	// 新内容的数据库路径带v1私有目录，凭据缺失也能与真正旧布局区分；不改公开文件名。
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
		format = "csv"
	}
	hash := sha256.New()
	err = (FormatWriter{}).Write(ctx, io.MultiWriter(file, hash), format, headers, lines)
	err = errors.Join(err, file.Close())
	if err != nil {
		return "", err
	}
	proof := artifactProof{Version: 1, TaskNo: row.TaskNo, Resource: row.Resource, Format: row.FileFormat, RequestedBy: row.RequestedBy, RowCount: uint32(len(records)), Scope: effectiveScope(row.Resource, scope), MaskedColumns: masked, SHA256: hex.EncodeToString(hash.Sum(nil))}
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

// stringifyCell 保留value原驱动表达：nil空串、字节为文本，其余按原数字／时间格式输出，无IO。
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

// removeArtifacts 仅在root内按row可信任务身份删除内容、凭据和两临时文件，返回IO错误。
// 不存在视为已清理，不删除任意数据库路径或其它任务文件；调用方持任务锁。
func removeArtifacts(root *os.Root, row ExportTaskRow) error {
	name, proof, err := fileNames(row, true)
	if err != nil {
		return err
	}
	var combined error
	// 回滚的pending尚未写file_path，也可能已有v1发布字节；两种可信布局均属本任务号。
	for _, path := range []string{name, proof, name + ".partial", proof + ".partial", filepath.Join("v1", name), filepath.Join("v1", proof), filepath.Join("v1", name+".partial"), filepath.Join("v1", proof+".partial")} {
		if err := root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			combined = errors.Join(combined, err)
		}
	}
	return combined
}

// verifyArtifact 在ctx／root下按row打开内容并复核当前scope及mask，返回已回到开头的句柄或拒绝／IO错误。
// 新凭据失配失败关闭；历史无凭据文件只供当前实际全局且无需脱敏的权限读取，受限下载须重新生成。
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
	if proof.Version != 1 || proof.TaskNo != row.TaskNo || proof.Resource != row.Resource || proof.Format != row.FileFormat || proof.RequestedBy != row.RequestedBy || proof.RowCount != row.RowCount {
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

// expectedPath 只返回s配置根目录内row可信身份的原存储表达；数据库路径仅用于一致性核对。
func (s ExportService) expectedPath(row ExportTaskRow) (string, error) {
	name, _, err := fileNames(row, false)
	if row.FilePath != nil && *row.FilePath == filepath.Join(s.exportDir(), "v1", name) {
		name = filepath.Join("v1", name)
	}
	return filepath.Join(s.exportDir(), name), err
}
