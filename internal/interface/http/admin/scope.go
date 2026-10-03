package admin

import (
	"math"
	"regexp"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// RequestIDPattern 校验 UUID 请求号。发券幂等回执、人工退款与设备导入都用它
// 判重，故放在共享层而非某一族内。
var RequestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// StationScope 读取当前登录账号的数据范围。站点、设备、计费方案等多个族都要
// 按同一份范围过滤，因此由 Deps 提供，而不是各族各自读一次。
func (d Deps) StationScope(c *gin.Context) (operations.DataScope, bool) {
	scope, err := ops.LoadDataScope(c.Request.Context(), d.Store.AdminDB, c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return scope, false
	}
	return scope, true
}

// RequireStationScope 在站点读写前核对目标站点是否落在数据范围内。
func (d Deps) RequireStationScope(c *gin.Context, id uint64) bool {
	scope, ok := d.StationScope(c)
	if !ok {
		return false
	}
	if !scope.AllowsStation(id) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return false
	}
	return true
}

// CanManageStationDefault 判断该范围能否改动整站默认配置：
// 只有不受限或无厂商限制的账号可以。
func CanManageStationDefault(scope operations.DataScope) bool {
	return scope.Unrestricted || len(scope.VendorIDs) == 0
}

// RequirePricingTargetScope 校验计费方案与分账模板的作用目标。
// 厂商数据范围受限时，整站默认配置不可改，只能改到本厂商名下的设备。
func (d Deps) RequirePricingTargetScope(c *gin.Context, stationID uint64, deviceID string) bool {
	scope, ok := d.StationScope(c)
	if !ok {
		return false
	}
	if !scope.AllowsStation(stationID) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return false
	}
	if CanManageStationDefault(scope) {
		return true
	}
	if deviceID == "" {
		httpapi.Write(c, 403, 1003, "厂商数据范围受限，不能修改整站默认配置", nil)
		return false
	}
	vendorID, err := ops.DeviceVendorID(c.Request.Context(), d.Store.AdminDB, stationID, deviceID)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return false
	}
	if !scope.AllowsVendor(vendorID) {
		httpapi.Write(c, 403, 1003, "该设备厂商不在您的数据范围内", nil)
		return false
	}
	return true
}

// QueryStationID 解析查询串里的 station_id。required 为假时缺省视为不过滤。
func QueryStationID(c *gin.Context, required bool) (uint64, bool) {
	values, exists := c.Request.URL.Query()["station_id"]
	if !exists && !required {
		return 0, true
	}
	if len(values) != 1 {
		httpapi.BadRequest(c, "station_id 必须为正整数")
		return 0, false
	}
	id, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || id == 0 || id > math.MaxInt64 {
		httpapi.BadRequest(c, "station_id 必须为正整数")
		return 0, false
	}
	return id, true
}
