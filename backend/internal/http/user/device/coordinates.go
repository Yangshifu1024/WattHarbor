package device

import (
	"math"
	"strconv"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/platform/httpapi"
)

// readCoordinates 解析 c 的可选经纬度，依次返回经度、纬度、是否提供和是否合法。
// 两项均缺省返回 0、0、false、true；单项缺失、格式或范围错误写 400 并返回 false、false。
func readCoordinates(c *gin.Context) (float64, float64, bool, bool) {
	rawLon, rawLat := c.Query("longitude"), c.Query("latitude")
	if rawLon == "" && rawLat == "" {
		return 0, 0, false, true
	}
	if rawLon == "" || rawLat == "" {
		httpapi.BadRequest(c, "经纬度需同时提供")
		return 0, 0, false, false
	}
	longitude, errLon := strconv.ParseFloat(rawLon, 64)
	latitude, errLat := strconv.ParseFloat(rawLat, 64)
	if errLon != nil || errLat != nil {
		httpapi.BadRequest(c, "经纬度格式无效")
		return 0, 0, false, false
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || math.IsNaN(latitude) || math.IsInf(latitude, 0) ||
		longitude < -180 || longitude > 180 || latitude < -90 || latitude > 90 {
		httpapi.BadRequest(c, "经纬度超出有效范围")
		return 0, 0, false, false
	}
	return longitude, latitude, true, true
}
