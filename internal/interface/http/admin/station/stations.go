package station

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
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
		stationFailure(c, err)
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
	scope, ok := admin.StationScope(c, a.DB)
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

// createStation 从 c 严格解析原站点资料，以实时权限调用新建用例，返回完整资料或原错误响应。
func (a API) createStation(c *gin.Context) { a.saveStation(c, 0) }

// updateStation 从 c 解析主键和完整资料，调用范围受限的更新用例，返回资料或原错误响应。
func (a API) updateStation(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	a.saveStation(c, id)
}

// saveStation 从 c 校验资料并读取实时范围，以 id 选择新建或更新；用例拥有事务，适配器只写 HTTP 响应。
func (a API) saveStation(c *gin.Context, id uint64) {
	var in device.StationInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if err := device.ValidateStationInput(in); err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	row, err := a.Stations.Save(c.Request.Context(), chargehttp.AdminAuditContext(c), scope, id, in)
	if err != nil {
		stationFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// stationFailure 将 err 的范围拒绝写入 c 的 403，其余沿用原资源错误；不执行 IO 或事务。
func stationFailure(c *gin.Context, err error) {
	if errors.Is(err, operations.ErrForbidden) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return
	}
	chargehttp.AdminResourceFailure(c, err)
}
