package station

import (
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// stations 分页返回站点列表。数据范围由管理员的站点授权决定，
// 没有授权时 device.ScopeStations 会把查询收窄到他能看的站点。
func (a API) stations(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "active disabled construction")
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	out, err := device.ListStations(c.Request.Context(), a.DB, q, scope)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(operations.Profile).Permissions
	httpapi.OK(c, out)
}

// station 按主键返回单个站点详情；已软删除的站点等同不存在。
func (a API) station(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	row, err := device.FindStation(c.Request.Context(), a.DB, id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// createStation 新建站点。站点是计费配置的挂载点，计费方案都挂在它上面。
func (a API) createStation(c *gin.Context) {
	var input device.StationInput
	if !chargehttp.DecodeResource(c, &input) {
		return
	}
	if input.Code != nil {
		httpapi.BadRequest(c, "编码已移除：站点统一使用主键 ID，请勿提交 code 字段")
		return
	}
	if !device.ValidStationInput(input) {
		httpapi.BadRequest(c, "站点参数无效：请检查名称、经纬度和状态")
		return
	}
	row := device.Station{Name: strings.TrimSpace(input.Name), Address: input.Address,
		Longitude: *input.Longitude, Latitude: *input.Latitude, Status: input.Status,
		ContactPhone: input.ContactPhone}
	p := c.MustGet("admin_profile").(operations.Profile)
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, p, "create", "station", row.ID, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// updateStation 改站点资料。站点主键不可变，改名改地址不影响挂在它上面的配置。
func (a API) updateStation(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var input device.StationInput
	if !chargehttp.DecodeResource(c, &input) {
		return
	}
	if input.Code != nil {
		httpapi.BadRequest(c, "编码已移除：站点统一使用主键 ID，请勿提交 code 字段")
		return
	}
	if !device.ValidStationInput(input) {
		httpapi.BadRequest(c, "站点参数无效：请检查名称、经纬度和状态")
		return
	}
	row := device.Station{ID: id, Name: strings.TrimSpace(input.Name), Address: input.Address,
		Longitude: *input.Longitude, Latitude: *input.Latitude, Status: input.Status,
		ContactPhone: input.ContactPhone}
	p := c.MustGet("admin_profile").(operations.Profile)
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		before, err := device.LockStationForUpdate(tx, id)
		if err != nil {
			return err
		}
		if err := device.UpdateStation(tx, id, map[string]any{
			"name": row.Name, "address": row.Address, "longitude": row.Longitude,
			"latitude": row.Latitude, "status": row.Status, "contact_phone": row.ContactPhone,
		}); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, p, "update", "station", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}
