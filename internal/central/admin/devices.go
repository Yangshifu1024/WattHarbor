package admin

import (
	"context"
	"encoding/json"
	"fmt"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	paystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"

	"time"
)

// devices 响应设备分页列表，状态入参只接受 enabled/disabled/retired/fault，
// 并把当前管理员的权限列表一并带回，供前端决定按钮的可见性。
func (a ResourceAPI) devices(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "enabled disabled retired fault")
	if !ok {
		return
	}
	q.StationID, ok = queryStationID(c, false)
	if !ok {
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	if q.StationID > 0 && !scope.AllowsStation(q.StationID) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return
	}
	out, err := paystore.ListDevices(c.Request.Context(), a.Store.AdminDB, q, scope)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(operations.Profile).Permissions
	a.enrichDevices(c.Request.Context(), out.Items)
	httpapi.OK(c, out)
}

// device 按设备编号（不是内部主键）返回单台设备详情，编号最长 64 字符。查不到由
// resourceFailure 回 404。
func (a ResourceAPI) device(c *gin.Context) {
	id := c.Param("id")
	if len(id) > 64 {
		httpapi.BadRequest(c, "设备编号过长")
		return
	}
	var row paystore.Device
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	query := paystore.ScopeVendors(scope, paystore.ScopeStations(scope, paystore.DeviceQuery(c.Request.Context(), a.Store.AdminDB), "d.station_id"), "d.vendor_id")
	if err := query.Select(paystore.DeviceColumns).Where("d.device_id = ?", id).Take(&row).Error; err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if err := row.NormalizeMetadata(); err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	cap, _ := charging.ProtocolCapabilities(row.ProtocolAdapter)
	row.ReportsEnergy = cap.ReportsEnergy
	row.ReportsSegmentedPower = cap.ReportsSegmentedPower
	rows := []paystore.Device{row}
	a.enrichDevices(c.Request.Context(), rows)
	httpapi.OK(c, rows[0])
}

// Enrich only devices already authorized by the central data scope, in one call.
// A gateway outage must not fabricate a 'never online' result or hide operations.
func (a ResourceAPI) enrichDevices(ctx context.Context, rows []paystore.Device) {
	for i := range rows {
		if rows[i].Ports == nil {
			rows[i].Ports = []paystore.DevicePortStatus{}
		}
	}
	if len(rows) == 0 || a.GatewayURL == "" || a.ServiceToken == "" {
		return
	}
	query := url.Values{}
	for _, row := range rows {
		query.Add("device_id", row.DeviceID)
	}
	var result struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				DeviceID        string                      `json:"device_id"`
				VendorName      *string                     `json:"vendor_name"`
				LastHeartbeatAt *time.Time                  `json:"last_heartbeat_at"`
				SignalStrength  *uint8                      `json:"signal_strength"`
				SignalAt        *time.Time                  `json:"signal_at"`
				Ports           []paystore.DevicePortStatus `json:"ports"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := (serviceclient.Client{Timeout: 5 * time.Second}).GetJSON(ctx, a.GatewayURL, a.ServiceToken, "/api/v1/internal/device-summaries?"+query.Encode(), &result); err != nil || result.Code != 0 {
		return
	}
	for i := range rows {
		for _, extra := range result.Data.Items {
			if extra.DeviceID == rows[i].DeviceID {
				rows[i].VendorName = extra.VendorName
				rows[i].LastHeartbeatAt = extra.LastHeartbeatAt
				rows[i].SignalStrength = extra.SignalStrength
				rows[i].SignalAt = extra.SignalAt
				rows[i].Ports = extra.Ports
				if rows[i].Ports == nil {
					rows[i].Ports = []paystore.DevicePortStatus{}
				}
				rows[i].RuntimeAvailable = true
				break
			}
		}
	}
}

// Operational suspension leaves the gateway connection and existing orders intact.
func (a ResourceAPI) setDeviceStatus(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Status string `json:"status"`
	}
	if id == "" || len(id) > 64 {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.Status != "enabled" && in.Status != "disabled" {
		httpapi.BadRequest(c, "运营状态只允许启用或禁用")
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	var row paystore.Device
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		locked, err := paystore.LockDeviceByID(c.Request.Context(), tx, scope, id)
		if err != nil {
			return err
		}
		row = locked
		if row.Status != "enabled" && row.Status != "disabled" {
			return errConflict
		}
		if err := row.NormalizeMetadata(); err != nil {
			return err
		}
		before := row
		row.Status = in.Status
		row.UpdatedAt = paystore.NextDeviceUpdateTime(row.UpdatedAt)
		if err := paystore.SaveDeviceStatus(tx, row.ID, in.Status, row.UpdatedAt); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "device.status", "device", row.ID, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

func (a ResourceAPI) updateDevice(c *gin.Context) {
	id := c.Param("id")
	if id == "" || len(id) > 64 {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	var input paystore.DeviceInput
	if !chargehttp.DecodeResource(c, &input) {
		return
	}
	metadata, expected, err := input.Metadata()
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	var row paystore.Device
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		query := paystore.ScopeVendors(scope, paystore.ScopeStations(scope, paystore.DeviceQuery(c.Request.Context(), tx), "d.station_id"), "d.vendor_id")
		if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Select(paystore.DeviceColumns).Where("d.device_id=?", id).Take(&row).Error; err != nil {
			return err
		}
		if err := row.NormalizeMetadata(); err != nil {
			return err
		}
		if !row.UpdatedAt.Equal(expected) {
			return fmt.Errorf("%w：设备资料已被修改，请刷新后重试", errConflict)
		}
		before := row
		row.Model, row.SerialNo = metadata.Model, metadata.SerialNo
		row.WarrantyUntil, row.Tags = metadata.WarrantyUntil, metadata.Tags
		row.UpdatedAt = paystore.NextDeviceUpdateTime(row.UpdatedAt)
		tags, err := json.Marshal(row.Tags)
		if err != nil {
			return err
		}
		if err := paystore.SaveDeviceMetadata(tx, row.ID, map[string]any{
			"model": row.Model, "serial_no": row.SerialNo,
			"warranty_until": row.WarrantyUntil, "tags_json": string(tags), "updated_at": row.UpdatedAt,
		}); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "device.update", "device", row.ID, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	cap, _ := charging.ProtocolCapabilities(row.ProtocolAdapter)
	row.ReportsEnergy, row.ReportsSegmentedPower = cap.ReportsEnergy, cap.ReportsSegmentedPower
	rows := []paystore.Device{row}
	a.enrichDevices(c.Request.Context(), rows)
	httpapi.OK(c, rows[0])
}
