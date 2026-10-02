package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	paystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strings"
)

// 站点统一按主键 ID 寻址，不保存业务编码 code。
// paystore.StationInput.Code 仅用于识别旧客户端请求，提交此字段时返回明确参数错误。

// stations 响应站点分页列表，状态入参只接受 active/disabled/construction，
// 并把当前管理员的权限列表一并带回，供前端决定按钮的可见性。
func (a ResourceAPI) stations(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "active disabled construction")
	if !ok {
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	out, err := paystore.ListStations(c.Request.Context(), a.Store.AdminDB, q, scope)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(operations.Profile).Permissions
	httpapi.OK(c, out)
}

// station 按主键返回单个站点详情；已软删除的站点等同不存在，由 resourceFailure 回 404。
func (a ResourceAPI) station(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	if !a.requireStationScope(c, id) {
		return
	}
	row, err := paystore.FindStation(c.Request.Context(), a.Store.AdminDB, id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// createStation 是新建站点的 HTTP 入口，转调 saveStation 的新建分支。
func (a ResourceAPI) createStation(c *gin.Context) { a.saveStation(c, true) }

// updateStation 是更新站点的 HTTP 入口，转调 saveStation 的更新分支。
func (a ResourceAPI) updateStation(c *gin.Context) { a.saveStation(c, false) }

// saveStation 创建或更新站点，并在同一事务内记录审计。
// 禁止 code 字段；更新分账模板必须使用独立绑定接口。更新先锁定站点，新建校验模板可用性。
func (a ResourceAPI) saveStation(c *gin.Context, create bool) {
	var id uint64
	if !create {
		var ok bool
		id, ok = chargehttp.AdminPathID(c)
		if !ok {
			return
		}
		if !a.requireStationScope(c, id) {
			return
		}
	}
	var input paystore.StationInput
	if !chargehttp.DecodeResource(c, &input) {
		return
	}
	if input.Code != nil {
		httpapi.BadRequest(c, "编码已移除：站点统一使用主键 ID，请勿提交 code 字段")
		return
	}
	if !paystore.ValidStationInput(input) || (!create && input.SplitTemplateID != nil) {
		httpapi.BadRequest(c, "站点参数无效：请检查名称、经纬度和状态")
		return
	}
	row := paystore.Station{ID: id, Name: strings.TrimSpace(input.Name), Address: input.Address, Longitude: *input.Longitude, Latitude: *input.Latitude, Status: input.Status, ContactPhone: input.ContactPhone, SplitTemplateID: input.SplitTemplateID}
	p := c.MustGet("admin_profile").(operations.Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if create {
			if input.SplitTemplateID != nil {
				if err := paystore.UsableTemplate(tx, *input.SplitTemplateID); err != nil {
					return err
				}
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			return chargehttp.AdminResourceAudit(tx, p, "create", "station", row.ID, nil, row, c.ClientIP(), httpapi.RequestID(c))
		}
		before, err := paystore.LockStationForUpdate(tx, id)
		if err != nil {
			return err
		}
		row.SplitTemplateID = before.SplitTemplateID
		if err := paystore.UpdateStation(tx, id, map[string]any{"name": row.Name, "address": row.Address, "longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status, "contact_phone": row.ContactPhone}); err != nil {
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
