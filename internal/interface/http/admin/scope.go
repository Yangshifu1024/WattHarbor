package admin

import (
	"math"
	"regexp"
	"strconv"

	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RequestIDPattern 校验 UUID 请求号。发券幂等回执、人工退款与设备导入都用它
// 判重，故放在共享层而非某一族内。
var RequestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// StationScope 读取当前登录账号的数据范围。站点、设备、计费方案等多个族都要
// 按当前账号的实时范围过滤。
func StationScope(c *gin.Context, db *gorm.DB) (operations.DataScope, bool) {
	scope, err := ops.LoadDataScope(c.Request.Context(), db, c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return scope, false
	}
	return scope, true
}

// RequireStationScope 在站点读写前核对目标站点是否落在数据范围内。
func RequireStationScope(c *gin.Context, db *gorm.DB, id uint64) bool {
	scope, ok := StationScope(c, db)
	if !ok {
		return false
	}
	if !scope.AllowsStation(id) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return false
	}
	return true
}

// RequirePricingTargetScope 校验计费方案与分账模板的作用目标。
// 厂商数据范围受限时，整站默认配置不可改，只能改到本厂商名下的设备。
func RequirePricingTargetScope(c *gin.Context, db *gorm.DB, stationID uint64, deviceID string) bool {
	scope, ok := StationScope(c, db)
	if !ok {
		return false
	}
	if err := pricing.CheckTargetScope(c.Request.Context(), db, scope, stationID, deviceID); err != nil {
		if errors.Is(err, operations.ErrForbidden) {
			httpapi.Write(c, 403, 1003, err.Error(), nil)
		} else {
			chargehttp.AdminResourceFailure(c, err)
		}
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
