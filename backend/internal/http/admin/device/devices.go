package device

import (
	"errors"

	"chargepilot/internal/device"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
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
	scope, ok := admin.StationScope(c, a.Scopes)
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

// device 读取当前管理员范围内的设备资料和状态摘要。
func (a API) device(c *gin.Context) {
	id := c.Param("id")
	if len(id) > 64 {
		httpapi.BadRequest(c, "设备编号过长")
		return
	}
	scope, ok := admin.StationScope(c, a.Scopes)
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

// setDeviceStatus 校验设备状态输入，更新当前管理员范围内的设备。
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
	scope, ok := admin.StationScope(c, a.Scopes)
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

// updateDevice 校验设备资料和版本，更新当前管理员范围内的设备。
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
	scope, ok := admin.StationScope(c, a.Scopes)
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

// managementFailure 将设备参数、权限和存储错误转换为接口响应。
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
