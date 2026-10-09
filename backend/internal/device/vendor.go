package device

import (
	"context"
	"errors"
	"log"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"gorm.io/gorm"
)

// VendorGateway 提供厂商列表、详情、创建和更新方法，不暴露传输配置或数据库。
type VendorGateway interface {
	// Vendors 查询公开厂商列表，不自动重试。
	Vendors(context.Context, contract.VendorListRequest, string) (contract.VendorListReply, error)
	// Vendor 按厂商 ID 查询公开资料。
	Vendor(context.Context, uint64, string) (contract.VendorReply, error)
	// CreateVendor 提交完整厂商资料，返回网关确认结果。
	CreateVendor(context.Context, contract.VendorRequest, string) (contract.VendorReply, error)
	// UpdateVendor 按指定厂商 ID 提交完整资料。
	UpdateVendor(context.Context, uint64, contract.VendorRequest, string) (contract.VendorReply, error)
}

// VendorService 核对中央账号厂商范围，厂商写入事务由网关管理。
type VendorService struct {
	DB      *gorm.DB      // central 审计及站点存在性连接，不访问 gateway 表
	Gateway VendorGateway // 由 cmd 注入，向网关查询和更新厂商资料
}

// VendorNotVisible 表示目标厂商超出范围，HTTP 返回 404。
type VendorNotVisible struct{}

// Error 返回厂商不可见提示。
func (VendorNotVisible) Error() string { return "厂商不存在或不在数据范围内" }

// ErrScopeConflict 表示范围类型、ID 或资源存在性校验失败。
var ErrScopeConflict = errors.New("数据范围与现有记录冲突")

// VendorQuery 将分页和账号厂商范围转为网关查询；不受限时不传范围 ID。
func VendorQuery(page persist.PageQuery, scope operations.DataScope) contract.VendorListRequest {
	q := contract.VendorListRequest{Page: page.Page, PageSize: page.PageSize, Keyword: page.Keyword, Status: page.Status}
	if !scope.Unrestricted && len(scope.VendorIDs) > 0 {
		q.IDs = append([]uint64(nil), scope.VendorIDs...)
	}
	return q
}

// List 核对当前权限后查询范围内厂商；导入选项只返回启用项。
// 上游返回越界或非启用选项时按依赖故障处理，中央操作权限仍由本地核对。
func (s VendorService) List(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, page persist.PageQuery, options bool) (persist.Page[contract.VendorReply], error) {
	permission := "vendor.read"
	if options {
		permission = "device.import"
		page.Status = "enabled"
	}
	if !audit.Actor.HasPermission(permission) {
		return persist.Page[contract.VendorReply]{}, operations.ErrForbidden
	}
	remote, err := s.Gateway.Vendors(ctx, VendorQuery(page, scope), audit.RequestID)
	if err != nil {
		return persist.Page[contract.VendorReply]{}, err
	}
	out := persist.Page[contract.VendorReply]{Items: remote.Items, Total: remote.Total, Page: remote.Page, PageSize: remote.PageSize}
	if out.Items == nil {
		out.Items = []contract.VendorReply{}
	}
	for _, row := range out.Items {
		if !scope.AllowsVendor(row.ID) || options && row.Status != "enabled" {
			return persist.Page[contract.VendorReply]{}, &contract.VendorFailure{Status: 503, Code: 5003, Message: "厂商服务暂时不可用，请稍后重试"}
		}
	}
	for _, p := range audit.Actor.Permissions {
		if p == "vendor.create" && !scope.Unrestricted && len(scope.VendorIDs) > 0 {
			continue
		}
		out.Permissions = append(out.Permissions, p)
	}
	return out, nil
}

// Get 核对当前读取权限后查询范围内厂商。
func (s VendorService) Get(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id uint64) (contract.VendorReply, error) {
	if !audit.Actor.HasPermission("vendor.read") {
		return contract.VendorReply{}, operations.ErrForbidden
	}
	if !scope.AllowsVendor(id) {
		return contract.VendorReply{}, VendorNotVisible{}
	}
	return s.Gateway.Vendor(ctx, id, audit.RequestID)
}

// Save 核对权限和范围后提交完整厂商资料，ID=0 创建，否则读取后按同一 ID 更新。
// 网关确认后尝试记录中央审计，审计失败不能回滚网关；网络结果未知时须核对厂商编码和资料。
func (s VendorService) Save(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id uint64, in contract.VendorRequest) (contract.VendorReply, error) {
	permission := "vendor.create"
	if id != 0 {
		permission = "vendor.update"
	}
	if !audit.Actor.HasPermission(permission) {
		return contract.VendorReply{}, operations.ErrForbidden
	}
	if id == 0 && !scope.Unrestricted && len(scope.VendorIDs) > 0 {
		return contract.VendorReply{}, operations.ErrForbidden
	}
	if id != 0 && !scope.AllowsVendor(id) {
		return contract.VendorReply{}, VendorNotVisible{}
	}
	var before any
	var out contract.VendorReply
	var err error
	action := "create"
	if id == 0 {
		out, err = s.Gateway.CreateVendor(ctx, in, audit.RequestID)
	} else {
		original, readErr := s.Gateway.Vendor(ctx, id, audit.RequestID)
		if readErr != nil {
			return contract.VendorReply{}, readErr
		}
		before, action = original, "update"
		out, err = s.Gateway.UpdateVendor(ctx, id, in, audit.RequestID)
	}
	if err != nil {
		return out, err
	}
	if err := ops.AppendAuditInTx(s.DB.WithContext(ctx), audit, ops.AuditChange{Action: action, Target: "vendor", ID: out.ID, Before: before, After: out}); err != nil {
		log.Printf("gateway audit failed action=%s target=vendor id=%d: %v", action, out.ID, err)
	}
	return out, nil
}

// ValidateScope 检查范围类型及 1–500 个 ID 是否存在。
// 厂商由网关核对总数，站点由中央仓储统计；按输入数量比对，不自动去重。
func (s VendorService) ValidateScope(ctx context.Context, scopeType string, ids []uint64) error {
	if !operations.ValidScopeType(scopeType) || !operations.ValidScopeIDs(ids) {
		return ErrScopeConflict
	}
	if scopeType == operations.ScopeVendor {
		page, err := s.Gateway.Vendors(ctx, VendorQuery(persist.PageQuery{Page: 1, PageSize: 1}, operations.DataScope{VendorIDs: ids}), "")
		if err != nil {
			return err
		}
		if int(page.Total) != len(ids) {
			return ErrScopeConflict
		}
		return nil
	}
	n, err := CountExistingStations(ctx, s.DB, ids)
	if err != nil {
		return err
	}
	if int(n) != len(ids) {
		return ErrScopeConflict
	}
	return nil
}
