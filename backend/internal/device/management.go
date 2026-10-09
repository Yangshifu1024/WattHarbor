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

// SummaryReader 仅提供已授权设备的运行摘要，不持有中央权限或业务写入能力。
type SummaryReader interface {
	// DeviceSummaries 在 context 下批量读取原编号的摘要，返回 nullable 事实或传输／服务错误。
	DeviceSummaries(context.Context, []string) ([]contract.DeviceSummary, error)
}

// ManagementService 拥有运营资料、启停及其审计；运行摘要只丰富已经通过范围过滤的设备。
type ManagementService struct {
	DB        *gorm.DB      // central 设备元数据连接，由进程管理
	Summaries SummaryReader // 网关具名客户端，由 cmd 注入；缺失或故障不补造已知状态
}

// List 在 ctx 下按 scope、q 返回未删除设备分页及运行摘要；显式越界站点或数据库故障返回错误。
// 摘要不可用时保留原运行未知语义；不修改设备或权限，摘要仅查询可见的当页编号。
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

// Get 在 ctx 下读取 scope 内 id 的资料和摘要；范围外与不存在同为缺失，存储失败原样返回。
// 仅查询已授权身份；网关故障保留未知字段，不影响中央运营资料可读性。
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

// Enrich 在 ctx 下以 rows 中已授权的编号批量读取摘要，原地合并命中项并将缺失端口规范为 []。
// 无返回值；依赖缺失或失败保留未知字段，额外设备不会扩大结果，nullable 零值原样保留。
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

// setCapabilities 从 row 的协议推导展示能力并原地写入；未知协议为零能力，无 IO。
func setCapabilities(row *Device) {
	cap, _ := charging.ProtocolCapabilities(row.ProtocolAdapter)
	row.ReportsEnergy, row.ReportsSegmentedPower = cap.ReportsEnergy, cap.ReportsSegmentedPower
}

// ChangeStatus 在 ctx 下以 audit 的实时权限变更 scope 内 id 的运营 status；只接受 enabled／disabled。
// 返回原资料、权限、缺失、状态冲突或存储错误。持行锁、更新时间与审计同事务，不关闭 TCP 或结束订单。
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

// Update 在 ctx 下以 audit 的实时权限提交 scope 内 id 的完整 in，核对锁定行的原毫秒版本。
// 返回资料及摘要，或输入、权限、缺失、旧版本、存储错误；只修改运营资料，资料与审计同事务。
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
