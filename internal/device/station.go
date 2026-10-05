package device

import (
	"context"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"gorm.io/gorm"
)

// ManagementInputError 保留运营资料输入失败的提示，HTTP 将其映射为 400。
type ManagementInputError struct {
	Message string // 字段校验的公开提示，不包含数据库细节
}

// Error 返回字段校验提示，不执行 IO。
func (e ManagementInputError) Error() string { return e.Message }

// StationService 拥有站点资料变更与审计的完整事务；已有站点读写必须核对显式范围。
type StationService struct {
	DB *gorm.DB // central 站点连接，由进程管理资源生命周期
}

// ValidateStationInput 检查 in 的废弃编码和必填资料；返回原公开参数错误，无 IO 或修改。
func ValidateStationInput(in StationInput) error {
	if in.Code != nil {
		return ManagementInputError{"编码已移除：站点统一使用主键 ID，请勿提交 code 字段"}
	}
	if !ValidStationInput(in) {
		return ManagementInputError{"站点参数无效：请检查名称、经纬度和状态"}
	}
	return nil
}

// Get 在 ctx 下读取 scope 内 id 的未删除站点；越界返回权限错误，不存在或存储失败原样返回。
func (s StationService) Get(ctx context.Context, scope operations.DataScope, id uint64) (Station, error) {
	if !scope.AllowsStation(id) {
		return Station{}, operations.ErrForbidden
	}
	return FindStation(ctx, s.DB, id)
}

// Save 在 ctx 下依据 audit 的实时权限保存 in，id=0 新建，否则必须属于 scope。
// 返回原完整资料或输入、权限、缺失、存储错误；资料与审计同事务。新建尚无站点身份，沿用 station.create 权限。
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

// StationInput 是站点新建／编辑完整请求，经纬度指针区分缺失和有效零值；Code 只用于拒绝旧编码。
type StationInput struct {
	// 保留已废弃的 Code 请求字段用于兼容性校验，禁止写入站点记录。
	Code         *string  `json:"code"`          // 兼容旧客户端的废弃字段；非 nil 时返回编码已移除的参数错误。
	Name         string   `json:"name"`          // 站点名称，必填，去空白后最长 128 字符。
	Address      *string  `json:"address"`       // 站点地址，可空，最长 255 字符。
	Longitude    *float64 `json:"longitude"`     // 经度，必填指针；缺失、非数字或超出 [-180,180] 判为无效。
	Latitude     *float64 `json:"latitude"`      // 纬度，必填指针；缺失、非数字或超出 [-90,90] 判为无效。
	Status       string   `json:"status"`        // 站点状态，必填：active / disabled / construction 三选一。
	ContactPhone *string  `json:"contact_phone"` // 联系电话，可空，最长 32 字符。
}

// ValidStationInput 以 input 为完整待写资料，返回其合法性，不执行 IO 或修改：名称必填且不超过 128 字符，经纬度必填、不能为 NaN 且落在
// 合法区间内，状态必须是 active/disabled/construction 之一，
// 地址和联系电话两个可空文本字段各有长度上限。站点全年无休，不配置营业时间。
func ValidStationInput(input StationInput) bool {
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 || input.Longitude == nil || input.Latitude == nil || math.IsNaN(*input.Longitude) || math.IsNaN(*input.Latitude) || math.Abs(*input.Longitude) > 180 || math.Abs(*input.Latitude) > 90 || input.Status == "" || !oneOfStatus(input.Status, "active disabled construction") {
		return false
	}
	for value, max := range map[*string]int{input.Address: 255, input.ContactPhone: 32} {
		if value != nil && utf8.RuneCountInString(*value) > max {
			return false
		}
	}
	return true
}

// oneOfStatus 判断 value 是否为空或属于 allowed 的空格分隔集合；无 IO 或修改。
func oneOfStatus(value, allowed string) bool {
	if value == "" {
		return true
	}
	for _, v := range strings.Fields(allowed) {
		if value == v {
			return true
		}
	}
	return false
}
