package station

import (
	"errors"

	"chargepilot/internal/device"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// stations 按分页、筛选和当前管理员站点范围查询站点，返回列表和权限。
func (a API) stations(c *gin.Context) {
	// 白名单是 station.status 的三值词表（AllStationStatuses）。
	q, ok := admin.ReadAdminPage(c, device.AllStationStatuses)
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	out, err := a.Stations.List(c.Request.Context(), q, scope)
	if err != nil {
		stationFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(operations.Profile).Permissions
	httpapi.OK(c, out)
}

// station 按主键返回单个站点详情；已软删除的站点等同不存在。
func (a API) station(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	row, err := a.Stations.Get(c.Request.Context(), scope, id)
	if err != nil {
		stationFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// createStation 创建站点并返回资料。
func (a API) createStation(c *gin.Context) { a.saveStation(c, 0) }

// updateStation 更新指定站点并返回资料。
func (a API) updateStation(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	a.saveStation(c, id)
}

// saveStation 校验站点资料和管理员当前范围；id 为零时新建，否则更新。
func (a API) saveStation(c *gin.Context, id uint64) {
	var in device.StationInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if err := device.ValidateStationInput(in); err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
	if !ok {
		return
	}
	row, err := a.Stations.Save(c.Request.Context(), admin.AdminAuditContext(c), scope, id, in)
	if err != nil {
		stationFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// stationFailure 将站点范围拒绝转换为 403，其余错误使用通用资源响应。
func stationFailure(c *gin.Context, err error) {
	if errors.Is(err, operations.ErrForbidden) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return
	}
	admin.AdminResourceFailure(c, err)
}
