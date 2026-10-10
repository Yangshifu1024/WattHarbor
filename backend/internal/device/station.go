package device

import (
	"context"
	"math"
	"strings"
	"unicode/utf8"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"gorm.io/gorm"
)

// ManagementInputError 保留运营资料输入失败的提示，HTTP 将其映射为 400。
type ManagementInputError struct {
	Message string // 字段校验的公开提示，不包含数据库细节
}

// Error 返回字段校验提示。
func (e ManagementInputError) Error() string { return e.Message }

// StationService 负责站点资料与审计事务，查询和更新均检查当前站点范围。
type StationService struct {
	DB *gorm.DB // central 站点连接，由进程管理资源生命周期
}

// List 按当前站点范围分页查询，无范围记录时不限制。
func (s StationService) List(ctx context.Context, q persist.PageQuery, scope operations.DataScope) (persist.Page[Station], error) {
	return ListStations(ctx, s.DB, q, scope)
}

// ValidateStationInput 检查完整资料，并拒绝已废弃的站点编码。
func ValidateStationInput(in StationInput) error {
	if in.Code != nil {
		return ManagementInputError{"编码已移除：站点统一使用主键 ID，请勿提交 code 字段"}
	}
	if !ValidStationInput(in) {
		return ManagementInputError{"站点参数无效：请检查名称、经纬度和状态"}
	}
	return nil
}

// Get 读取范围内未删除站点；越界返回权限错误。
func (s StationService) Get(ctx context.Context, scope operations.DataScope, id uint64) (Station, error) {
	if !scope.AllowsStation(id) {
		return Station{}, operations.ErrForbidden
	}
	return FindStation(ctx, s.DB, id)
}

// Save 核对当前权限后保存完整站点资料与审计，ID=0 创建，否则检查站点范围。
// 新站点按 station.create 权限授权。
func (s StationService) Save(ctx context.Context, audit ops.AuditContext, scope operations.DataScope, id uint64, in StationInput) (Station, error) {
	permission := "station.create"
	if id != 0 {
		permission = "station.update"
	}
	if !audit.Actor.HasPermission(permission) || id != 0 && !scope.AllowsStation(id) {
		return Station{}, operations.ErrForbidden
	}
	if err := ValidateStationInput(in); err != nil {
		return Station{}, err
	}
	row := Station{ID: id, Name: strings.TrimSpace(in.Name), Address: in.Address,
		Longitude: *in.Longitude, Latitude: *in.Latitude, Status: in.Status, ContactPhone: in.ContactPhone}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before any
		action := "create"
		if id == 0 {
			if err := CreateStationInTx(tx, &row); err != nil {
				return err
			}
		} else {
			locked, err := LockStationForUpdate(tx, id)
			if err != nil {
				return err
			}
			before, action = locked, "update"
			if err := UpdateStation(tx, row); err != nil {
				return err
			}
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: action, Target: "station", ID: row.ID, Before: before, After: row})
	})
	return row, err
}

// StationInput 是站点完整资料，经纬度用指针区分缺失和零值；Code 仅用于拒绝旧请求。
type StationInput struct {
	// 保留已废弃的 Code 请求字段用于兼容性校验，禁止写入站点记录。
	Code         *string       `json:"code"`          // 兼容旧客户端的废弃字段；非 nil 时返回编码已移除的参数错误。
	Name         string        `json:"name"`          // 站点名称，非空且提交文本最多 128 字符；保存时去除首尾空白。
	Address      *string       `json:"address"`       // 站点地址，可空，最长 255 字符。
	Longitude    *float64      `json:"longitude"`     // 经度，必填指针；缺失、非数字或超出 [-180,180] 判为无效。
	Latitude     *float64      `json:"latitude"`      // 纬度，必填指针；缺失、非数字或超出 [-90,90] 判为无效。
	Status       StationStatus `json:"status"`        // 站点状态，必填：active / disabled / construction 三选一。
	ContactPhone *string       `json:"contact_phone"` // 联系电话，可空，最长 32 字符。
}

// ValidStationInput 检查名称必填且最多 128 字、经纬度必填且范围有效，禁止 NaN。
// 状态仅允许 station.status 三值（见 StationStatus），地址和电话分别最多 255 和 32 字符。
func ValidStationInput(input StationInput) bool {
	// 空串与 oneOfStatus 的空值放行语义相反：站点状态必填，先单独拒绝空串再判断词表，两者不可合并。
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 || input.Longitude == nil || input.Latitude == nil || math.IsNaN(*input.Longitude) || math.IsNaN(*input.Latitude) || math.Abs(*input.Longitude) > 180 || math.Abs(*input.Latitude) > 90 || input.Status == "" || !oneOfStatus(input.Status, AllStationStatuses) {
		return false
	}
	for value, max := range map[*string]int{input.Address: 255, input.ContactPhone: 32} {
		if value != nil && utf8.RuneCountInString(*value) > max {
			return false
		}
	}
	return true
}

// oneOfStatus 检查值为空或属于空格分隔的允许状态集合，只用于 station.status 的写入准入。
// 空串返回 true 表示“不属于任何词表之外的误填”，与调用方「状态必填」的显式拒绝并存，两者语义不同，不可合并。
func oneOfStatus(value StationStatus, allowed string) bool {
	if value == "" {
		return true
	}
	for _, v := range strings.Fields(allowed) {
		if string(value) == v {
			return true
		}
	}
	return false
}
