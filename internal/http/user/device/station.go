package device

import (
	"errors"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// nearbyStations 从c解析原分页／定位／半径，调用所属服务并返回原公开投影或错误。
func (a API) nearbyStations(c *gin.Context) {
	longitude, latitude, located, ok := readCoordinates(c)
	if !ok {
		return
	}
	fallback := 20
	if !located {
		fallback = 10
	}
	page, size, ok := httpapi.ReadPaging(c, fallback, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	radius := 50.0
	if raw := c.Query("radius_km"); raw != "" {
		var err error
		radius, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			httpapi.BadRequest(c, "搜索半径须为1至50公里")
			return
		}
	}
	out, err := a.Stations.Nearby(c.Request.Context(), device.StationQuery{Page: page, PageSize: size, Located: located, Longitude: longitude, Latitude: latitude, RadiusKM: radius})
	if err != nil {
		var input *device.StationInputError
		if errors.As(err, &input) {
			httpapi.BadRequest(c, input.Message)
			return
		}
		httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
		return
	}
	httpapi.OK(c, out)
}

// stationDetail 从c解析原站点ID并调用完整组合用例，不在HTTP读取或推断业务事实。
func (a API) stationDetail(c *gin.Context) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "站点 ID 无效")
		return
	}
	out, err := a.Stations.Detail(c.Request.Context(), id)
	if err == nil {
		httpapi.OK(c, out)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "站点不存在", nil)
		return
	}
	message := "站点暂时无法读取"
	var failure *device.StationQueryError
	if errors.As(err, &failure) {
		if failure.Stage == "announcements" {
			message = "站点公告暂时无法读取"
		}
		if failure.Stage == "devices" {
			message = "站点设备暂时无法读取"
		}
	}
	httpapi.Write(c, 503, 5003, message, nil)
}
