package device

import (
	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
)

// ScopeStations 为指定站点列追加范围过滤；column 由调用方提供，如 t.station_id。
// 不受限或站点范围为空时不追加条件。
func ScopeStations(scope operations.DataScope, query *gorm.DB, column string) *gorm.DB {
	if scope.Unrestricted || len(scope.StationIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", scope.StationIDs)
}

// ScopeVendors 对归属厂商的资源用同样的方式收窄查询。
func ScopeVendors(scope operations.DataScope, query *gorm.DB, column string) *gorm.DB {
	if scope.Unrestricted || len(scope.VendorIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", scope.VendorIDs)
}
