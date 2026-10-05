package admin

import (
	"math"
	"regexp"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RequestIDPattern 校验UUID请求号的文本形状，供发券、人工退款和设备导入的HTTP输入准入。
// 实际幂等身份及内容比对由所属业务服务执行。
var RequestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// StationScope 在c请求取消范围内通过db读取已鉴权账号当前数据范围。
// 返回范围与成功标志；读取失败写原资源错误并返回false，调用方立即返回。
// 沿用无范围记录表示不受限的规则，不将它解释为无权读取所有站点。
func StationScope(c *gin.Context, db *gorm.DB) (operations.DataScope, bool) {
	scope, err := ops.LoadDataScope(c.Request.Context(), db, c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		AdminResourceFailure(c, err)
		return scope, false
	}
	return scope, true
}

// QueryStationID 从c解析单个正整数station_id，required指定是否必填，上限MaxInt64。
// 成功返回站点号和true，可选缺省为0／true；非法输入写400并返回0／false。
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
