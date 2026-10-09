package admin

import (
	"math"
	"regexp"
	"strconv"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// RequestIDPattern 校验发券、人工退款和设备导入请求号是否符合 UUID 格式。
// 实际去重标识及内容比对由业务服务执行。
var RequestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// StationScope 在 c 请求取消范围内通过 reader 读取已鉴权账号当前数据范围。
// 返回范围与成功标志；读取失败写资源错误并返回 false，调用方立即返回。
// 沿用无范围记录表示不受限的规则，不将它解释为无权读取所有站点。
func StationScope(c *gin.Context, reader ScopeReader) (operations.DataScope, bool) {
	scope, err := reader.Load(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		AdminResourceFailure(c, err)
		return scope, false
	}
	return scope, true
}

// QueryStationID 从请求解析单个正整数 station_id，required 指定是否必填，上限 MaxInt64。
// 成功返回站点号和 true，可选缺省为 0／true；非法输入写 400 并返回 0／false。
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
