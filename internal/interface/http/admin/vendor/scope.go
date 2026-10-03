package vendor

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
)

// ErrScopeConflict 表示范围配置本身不合法（类型未知、ID 不存在、数量越界），统一按 409 返回。
var ErrScopeConflict = errors.New("数据范围与现有记录冲突")

// ValidateScope 校验范围类型为 station 或 vendor，ID 数量为 1–500，且对应记录存在。
// 空范围表示不受限，保存前必须排除无效 ID。
//
// 厂商档��归网关所有，因此校验厂商范围要经服务接口回网关核对，不直读别库。
func (a API) ValidateScope(ctx context.Context, scopeType string, ids []uint64) error {
	if !operations.ValidScopeType(scopeType) || !operations.ValidScopeIDs(ids) {
		return ErrScopeConflict
	}
	if scopeType == operations.ScopeVendor {
		var page admin.Page[VendorRow]
		query := VendorQuery(admin.PageQuery{Page: 1, PageSize: 1}, operations.DataScope{VendorIDs: ids})
		if err := a.RequestVendor(ctx, "GET", "", query, nil, &page, ""); err != nil {
			return err
		}
		if int(page.Total) != len(ids) {
			return ErrScopeConflict
		}
		return nil
	}
	found, err := device.CountExistingStations(ctx, a.Store.AdminDB, ids)
	if err != nil {
		return err
	}
	if int(found) != len(ids) {
		return ErrScopeConflict
	}
	return nil
}
