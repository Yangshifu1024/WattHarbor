package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
)

// errScopeConflict 表示范围配置本身不合法（类型未知、ID 不存在、数量越界），统一按 409 返回。
var errScopeConflict = errors.New("数据范围与现有记录冲突")

// validateScope 校验范围类型为 station 或 vendor，ID 数量为 1–500，且对应记录存在。
// 空范围表示不受限，保存前必须排除无效 ID。
func (a ResourceAPI) validateScope(ctx context.Context, scopeType string, ids []uint64) error {
	if !operations.ValidScopeType(scopeType) || !operations.ValidScopeIDs(ids) {
		return errScopeConflict
	}
	if scopeType == operations.ScopeVendor {
		var page Page[vendorRow]
		query := vendorQuery(PageQuery{Page: 1, PageSize: 1}, operations.DataScope{VendorIDs: ids})
		if err := a.requestVendor(ctx, "GET", "", query, nil, &page, ""); err != nil {
			return err
		}
		if int(page.Total) != len(ids) {
			return errScopeConflict
		}
		return nil
	}
	var found int64
	if err := a.Store.AdminDB.WithContext(ctx).Table("station").Where("id IN ? AND deleted_at IS NULL", ids).Count(&found).Error; err != nil {
		return err
	}
	if int(found) != len(ids) {
		return errScopeConflict
	}
	return nil
}

// decodeScopeList 解析范围 ID 数组：空白按"未填"处理返回空列表，
// 非法 JSON 交回调用方报错，不静默变成空范围。
func decodeScopeList(raw string) ([]uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []uint64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}
