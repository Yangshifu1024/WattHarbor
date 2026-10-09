package central

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidResponse 表示响应状态、必需字段或字段值不符合接口要求。
	ErrInvalidResponse = errors.New("invalid central response")
	// ErrAuthorizationDenied 表示订单不允许启动或停止，或授权响应的订单编号不匹配。
	ErrAuthorizationDenied = errors.New("central charge authorization denied")
	// ErrResultConflict 表示 central 拒绝了与已保存记录冲突的设备回执。
	ErrResultConflict = errors.New("central rejected device result")
)

// BusinessError 保存 central 返回的非零业务错误码。
type BusinessError struct{ Code int }

// Error 返回包含业务错误码的文本。
func (e BusinessError) Error() string {
	return fmt.Sprintf("central rejected request: code %d", e.Code)
}
