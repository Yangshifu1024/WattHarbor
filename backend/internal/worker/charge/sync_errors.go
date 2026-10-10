package charge

import (
	"errors"
	"fmt"
	"strings"
)

// syncBatchErrors 保存批内原因供 errors.Is/As 检查；日志仅输出失败数量和首项摘要。
// 生命周期限于一批，不写日志；原始原因不因摘要截断而丢失。
type syncBatchErrors struct {
	causes []error // 按失败记录顺序保存业务及退避错误，一批最多50条
}

// add 登记一条失败记录；kind/key 用于首项定位，cause 与退避保存错误都保留错误链。
func (e *syncBatchErrors) add(kind, key string, cause, deferErr error) {
	e.causes = append(e.causes, fmt.Errorf("%s %s: %w", kind, key, errors.Join(cause, deferErr)))
}

// err 无失败时返回 nil，否则返回本批的摘要错误；调用后不再修改 e。
func (e *syncBatchErrors) err() error {
	if len(e.causes) == 0 {
		return nil
	}
	return e
}

// Error 将首项原因压缩成单行最多256个字符，避免50条失败在一条日志中展开。
func (e *syncBatchErrors) Error() string {
	first := []rune(strings.Join(strings.Fields(e.causes[0].Error()), " "))
	if len(first) > 256 {
		first = append(first[:256], '…')
	}
	return fmt.Sprintf("sync batch: %d failed; first: %s", len(e.causes), string(first))
}

// Unwrap 保留每条具体原因，供调用方检查中央冲突、取消及持久化错误。
func (e *syncBatchErrors) Unwrap() []error { return e.causes }
