package device

import (
	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
)

// ScopeStations 给指定站点列追加范围条件；不受限或站点范围为空时不加条件。
func ScopeStations(scope operations.DataScope, query *gorm.DB, column string) *gorm.DB {
	if scope.Unrestricted || len(scope.StationIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", scope.StationIDs)
}

// ScopeVendors 按允许的厂商范围限制资源查询。
func ScopeVendors(scope operations.DataScope, query *gorm.DB, column string) *gorm.DB {
	if scope.Unrestricted || len(scope.VendorIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", scope.VendorIDs)
}
