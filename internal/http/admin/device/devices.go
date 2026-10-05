package device

import (
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// devices 响应设备分页列表，状态入参只接受 enabled/disabled/retired/fault，
// 并把当前管理员的权限列表一并带回，供前端决定按钮的可见性。
func (a API) devices(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, "enabled disabled retired fault")
	if !ok {
		return
	}
	q.StationID, ok = admin.QueryStationID(c, false)
	if !ok {
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	out, err := a.Management.List(c.Request.Context(), scope, q)
	if err != nil {
		managementFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(operations.Profile).Permissions
	httpapi.OK(c, out)
}

// device 从 c 解析设备号及实时范围，返回范围内完整资料与摘要或原错误；业务读取由用例执行。
func (a API) device(c *gin.Context) {
	id := c.Param("id")
	if len(id) > 64 {
		httpapi.BadRequest(c, "设备编号过长")
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	row, err := a.Management.Get(c.Request.Context(), scope, id)
	if err != nil {
		managementFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// setDeviceStatus 从 c 严格校验编号及启停输入，读取实时范围后调用用例，返回资料或原 HTTP 错误。
func (a API) setDeviceStatus(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Status string `json:"status"` /* 仅运营启停，不允许故障／退役替代 */
	}
	if id == "" || len(id) > 64 {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if in.Status != "enabled" && in.Status != "disabled" {
		httpapi.BadRequest(c, "运营状态只允许启用或禁用")
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	row, err := a.Management.ChangeStatus(c.Request.Context(), admin.AdminAuditContext(c), scope, id, in.Status)
	if err != nil {
		managementFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// updateDevice 从 c 校验完整资料和原版本，读取实时范围后调用更新用例，返回资料、摘要或原错误。
func (a API) updateDevice(c *gin.Context) {
	id := c.Param("id")
	if id == "" || len(id) > 64 {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	var in device.DeviceInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if _, _, err := in.Metadata(); err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	row, err := a.Management.Update(c.Request.Context(), admin.AdminAuditContext(c), scope, id, in)
	if err != nil {
		managementFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// managementFailure 将 err 的输入及权限分类写入 c，其余沿用原资源错误映射；不暴露存储细节。
func managementFailure(c *gin.Context, err error) {
	var input device.ManagementInputError
	switch {
	case errors.As(err, &input):
		httpapi.BadRequest(c, input.Error())
	case errors.Is(err, operations.ErrForbidden):
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
	default:
		admin.AdminResourceFailure(c, err)
	}
}
