package device

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"gorm.io/gorm"
)

// SummaryReader 仅查询已授权设备的运行摘要。
type SummaryReader interface {
	// DeviceSummaries 批量读取设备摘要，保留未上报字段的空值。
	DeviceSummaries(context.Context, []string) ([]contract.DeviceSummary, error)
}

// ManagementService 负责设备运营资料、启停和审计，运行摘要只补充范围内设备。
type ManagementService struct {
	DB        *gorm.DB      // central 设备元数据连接，由进程管理
	Summaries SummaryReader // 由 cmd 注入；未配置或查询失败时保留未知的运行状态
}

// List 分页读取范围内设备，仅向网关查询当页可见设备摘要。
// 摘要失败时保留未知字段，显式越界站点或中央查询失败仍返回错误。
func (s ManagementService) List(ctx context.Context, scope operations.DataScope, q persist.PageQuery) (persist.Page[Device], error) {
	if q.StationID > 0 && !scope.AllowsStation(q.StationID) {
		return persist.Page[Device]{}, operations.ErrForbidden
	}
	out, err := ListDevices(ctx, s.DB, q, scope)
	if err == nil {
		s.Enrich(ctx, out.Items)
	}
	return out, err
}

// Get 读取范围内设备和摘要；范围外与不存在均按缺失处理，网关失败时仍返回中央资料和未知运行字段。
func (s ManagementService) Get(ctx context.Context, scope operations.DataScope, id string) (Device, error) {
	row, err := FindDeviceByID(ctx, s.DB, scope, id)
	if err != nil {
		return row, err
	}
	if err := row.NormalizeMetadata(); err != nil {
		return row, err
	}
	setCapabilities(&row)
	rows := []Device{row}
	s.Enrich(ctx, rows)
	return rows[0], nil
}

// Enrich 批量查询已授权设备摘要并合并，缺失端口统一为 []。
// 摘要失败保留未知字段，额外设备不加入结果，可空零值不改写。
func (s ManagementService) Enrich(ctx context.Context, rows []Device) {
	for i := range rows {
		if rows[i].Ports == nil {
			rows[i].Ports = []DevicePortStatus{}
		}
	}
	if len(rows) == 0 || s.Summaries == nil {
		return
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.DeviceID
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	extras, err := s.Summaries.DeviceSummaries(ctx, ids)
	if err != nil {
		return
	}
	for i := range rows {
		for _, extra := range extras {
			if extra.DeviceID != rows[i].DeviceID {
				continue
			}
			rows[i].VendorName, rows[i].LastHeartbeatAt = extra.VendorName, extra.LastHeartbeatAt
			rows[i].SignalStrength, rows[i].SignalAt = extra.SignalStrength, extra.SignalAt
			rows[i].Ports = make([]DevicePortStatus, 0, len(extra.Ports))
			for _, port := range extra.Ports {
				rows[i].Ports = append(rows[i].Ports, DevicePortStatus{PortNo: port.PortNo, StatusCode: port.StatusCode, StatusAt: port.StatusAt})
			}
			rows[i].RuntimeAvailable = true
			break
		}
	}
}

// setCapabilities 按设备协议填写展示能力，未知协议使用零能力。
func setCapabilities(row *Device) {
	cap, _ := charging.ProtocolCapabilities(row.ProtocolAdapter)
	row.ReportsEnergy, row.ReportsSegmentedPower = cap.ReportsEnergy, cap.ReportsSegmentedPower
}

// ChangeStatus 核对当前权限和范围后锁定设备，只允许 enabled／disabled。
// 状态、更新时间和审计同事务，不关闭 TCP 或结束订单。
func (s ManagementService) ChangeStatus(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id, status string) (Device, error) {
	if id == "" || len(id) > 64 {
		return Device{}, ManagementInputError{"设备编号无效"}
	}
	if status != "enabled" && status != "disabled" {
		return Device{}, ManagementInputError{"运营状态只允许启用或禁用"}
	}
	if !audit.Actor.HasPermission("device.operate") {
		return Device{}, operations.ErrForbidden
	}
	var row Device
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := LockDeviceByID(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		row = locked
		if row.Status != "enabled" && row.Status != "disabled" {
			return operations.ErrConflict
		}
		if err := row.NormalizeMetadata(); err != nil {
			return err
		}
		before := row
		row.Status, row.UpdatedAt = status, NextDeviceUpdateTime(row.UpdatedAt)
		if err := SaveDeviceStatus(tx, row.ID, row.Status, row.UpdatedAt); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "device.status", Target: "device", ID: row.ID, Before: before, After: row})
	})
	return row, err
}

// Update 核对权限和范围后锁定设备，比较 UTC 毫秒版本，再保存完整运营资料和审计。
// 旧版本拒绝覆盖，成功后返回资料及摘要。
func (s ManagementService) Update(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id string, in DeviceInput) (Device, error) {
	if id == "" || len(id) > 64 {
		return Device{}, ManagementInputError{"设备编号无效"}
	}
	metadata, expected, err := in.Metadata()
	if err != nil {
		return Device{}, ManagementInputError{err.Error()}
	}
	if !audit.Actor.HasPermission("device.operate") {
		return Device{}, operations.ErrForbidden
	}
	var row Device
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := LockDeviceByID(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		row = locked
		if err := row.NormalizeMetadata(); err != nil {
			return err
		}
		if !row.UpdatedAt.Equal(expected) {
			return fmt.Errorf("%w：设备资料已被修改，请刷新后重试", operations.ErrConflict)
		}
		before := row
		row.Model, row.SerialNo = metadata.Model, metadata.SerialNo
		row.WarrantyUntil, row.Tags = metadata.WarrantyUntil, metadata.Tags
		row.UpdatedAt = NextDeviceUpdateTime(row.UpdatedAt)
		tags, err := json.Marshal(row.Tags)
		if err != nil {
			return err
		}
		if err := SaveDeviceMetadata(tx, row.ID, DeviceMetadataWrite{Model: row.Model, SerialNo: row.SerialNo, WarrantyUntil: row.WarrantyUntil, TagsJSON: string(tags), UpdatedAt: row.UpdatedAt}); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "device.update", Target: "device", ID: row.ID, Before: before, After: row})
	})
	if err != nil {
		return row, err
	}
	setCapabilities(&row)
	rows := []Device{row}
	s.Enrich(ctx, rows)
	return rows[0], nil
}
