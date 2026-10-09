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

// VendorGateway 提供网关所属字典的四个具名操作，不暴露传输配置或数据库。
type VendorGateway interface {
	// Vendors 在 context 下按查询及追踪号返回公开分页或原服务错误，无自动重试。
	Vendors(context.Context, contract.VendorListRequest, string) (contract.VendorListReply, error)
	// Vendor 在 context 下按厂商 ID 及追踪号返回原公开字典或错误。
	Vendor(context.Context, uint64, string) (contract.VendorReply, error)
	// CreateVendor 在 context 下提交完整输入及追踪号，返回网关确认的字典或错误。
	CreateVendor(context.Context, contract.VendorRequest, string) (contract.VendorReply, error)
	// UpdateVendor 在 context 下按原 ID 提交完整输入及追踪号，返回字典或错误。
	UpdateVendor(context.Context, uint64, contract.VendorRequest, string) (contract.VendorReply, error)
}

// VendorService 拥有中央账号对厂商的范围编排；厂商写入事务由网关提供。
type VendorService struct {
	DB      *gorm.DB      // central 审计及站点存在性连接，不访问 gateway 表
	Gateway VendorGateway // 具名厂商客户端，由 cmd 注入
}

// VendorNotVisible 表示原详情或编辑目标超出厂商范围；HTTP 沿用 404，避免扩大可见性。
type VendorNotVisible struct{}

// Error 返回原范围缺失提示，无 IO。
func (VendorNotVisible) Error() string { return "厂商不存在或不在数据范围内" }

// ErrScopeConflict 是数据范围类型、ID 或存在性失败的原公开错误。
var ErrScopeConflict = errors.New("数据范围与现有记录冲突")

// VendorQuery 以 page 及当前 scope 生成具名查询；范围不受限时不转发 ID，不修改输入或执行 IO。
func VendorQuery(page persist.PageQuery, scope operations.DataScope) contract.VendorListRequest {
	q := contract.VendorListRequest{Page: page.Page, PageSize: page.PageSize, Keyword: page.Keyword, Status: page.Status}
	if !scope.Unrestricted && len(scope.VendorIDs) > 0 {
		q.IDs = append([]uint64(nil), scope.VendorIDs...)
	}
	return q
}

// List 在 ctx 下以 audit 实时权限及 scope 查询 page，options 只返回启用的导入选项。
// 返回分页及本地权限或权限／服务错误；上游越界或非启用选项按依赖故障处理，不能授予中央权限。
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

// Get 在 ctx 下以 audit 的实时读取权限查询 scope 内 id；返回公开字典、权限、范围缺失或服务错误，只读。
func (s VendorService) Get(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id uint64) (contract.VendorReply, error) {
	if !audit.Actor.HasPermission("vendor.read") {
		return contract.VendorReply{}, operations.ErrForbidden
	}
	if !scope.AllowsVendor(id) {
		return contract.VendorReply{}, VendorNotVisible{}
	}
	return s.Gateway.Vendor(ctx, id, audit.RequestID)
}

// Save 在 ctx 下以 audit 实时权限及 scope 提交完整 in；id=0 新建，否则读取原字典后按原 ID 更新。
// 返回网关确认的公开字典或权限、范围缺失、原服务错误；中央审计沿用提交后尽力记录，不能回滚网关写入。
// 不添加重试或新业务 ID；网络结果未知时由调用方核对原厂商编码及资料。
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

// ValidateScope 在 ctx 下核对 scopeType 及一至五百个 ids；返回格式、存在性或存储／服务错误，只读。
// 厂商只经具名网关查询核对总数，站点由所属 repo 统计；沿用原输入数量与总数比对，不自行去重。
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
