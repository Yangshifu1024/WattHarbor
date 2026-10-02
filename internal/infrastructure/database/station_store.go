package database

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"math"
	"strings"
	"unicode/utf8"
)

// Station 是站点的对外模型，映射 central_db 的 station 表。站点是整套计费配置的
// 挂载点——分账模板、计费规则、充电套餐都挂在站点 ID 上。站点已无独立的业务编码
// 字段，列表、详情和所有下级配置的引用一律使用主键 ID。
type Station struct {
	ID              uint64  `json:"id" gorm:"primaryKey"`                              // 站点主键，也是分账模板、计费规则、充电套餐引用站点的外键。
	Name            string  `json:"name"`                                              // 站点名称，列表展示与关键词搜索都匹配它。
	Address         *string `json:"address"`                                           // 站点地址，可空；关键词搜索会同时匹配名称和地址。
	Longitude       float64 `json:"longitude"`                                         // 经度，有效范围 [-180,180]，站点地图定位用。
	Latitude        float64 `json:"latitude"`                                          // 纬度，有效范围 [-90,90]。
	Status          string  `json:"status"`                                            // 站点状态：active 营业中、disabled 停用、construction 建设中。
	ContactPhone    *string `json:"contact_phone"`                                     // 对外联系电话，可空，最长 32 字符。
	SplitTemplateID *uint64 `json:"split_template_id" gorm:"column:split_template_id"` // 已绑定的分账模板 ID，可空表示未绑定；只能在独立的绑定接口里改，不随站点编辑变更。
}

// TableName 指定 Station 落在 central_db 的 station 表上。
func (Station) TableName() string { return "station" }

// StationInput 是站点新建/编辑的请求体。经度、纬度用指针是为了区分"没传"和
// "传了 0"——这两个字段是必填项，缺失按无效处理。分账模板同样用指针表示"不绑定"，
// 但它只在新建时生效，编辑路径一旦带上就会被拒绝。
type StationInput struct {
	// 保留已废弃的 Code 请求字段用于兼容性校验，禁止写入站点记录。
	Code            *string  `json:"code"`              // 兼容旧客户端的废弃字段；非 nil 时返回编码已移除的参数错误。
	Name            string   `json:"name"`              // 站点名称，必填，去空白后最长 128 字符。
	Address         *string  `json:"address"`           // 站点地址，可空，最长 255 字符。
	Longitude       *float64 `json:"longitude"`         // 经度，必填指针；缺失、非数字或超出 [-180,180] 判为无效。
	Latitude        *float64 `json:"latitude"`          // 纬度，必填指针；缺失、非数字或超出 [-90,90] 判为无效。
	Status          string   `json:"status"`            // 站点状态，必填：active / disabled / construction 三选一。
	ContactPhone    *string  `json:"contact_phone"`     // 联系电话，可空，最长 32 字符。
	SplitTemplateID *uint64  `json:"split_template_id"` // 分账模板 ID，可空；指向 0 视为无效，新建时还会校验模板可用，编辑时携带该字段直接拒绝。
}

// ValidStationInput 校验站点写入参数：名称必填且不超过 128 字符，经纬度必填、不能为 NaN 且落在
// 合法区间内，状态必须是 active/disabled/construction 之一，分账模板 ID 不得为 0，
// 地址和联系电话两个可空文本字段各有长度上限。站点全年无休，不配置营业时间。
func ValidStationInput(input StationInput) bool {
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 || input.Longitude == nil || input.Latitude == nil || math.IsNaN(*input.Longitude) || math.IsNaN(*input.Latitude) || math.Abs(*input.Longitude) > 180 || math.Abs(*input.Latitude) > 90 || input.Status == "" || !oneOfStatus(input.Status, "active disabled construction") {
		return false
	}
	if input.SplitTemplateID != nil && *input.SplitTemplateID == 0 {
		return false
	}
	for value, max := range map[*string]int{input.Address: 255, input.ContactPhone: 32} {
		if value != nil && utf8.RuneCountInString(*value) > max {
			return false
		}
	}
	return true
}

// Stations 分页查询未删除的站点，支持按状态过滤和按名称/地址模糊搜索（通配符已转义），
// 先 Count 再按 ID 倒序取当页，最后把分页信息原样带回给调用方组装响应。
func ListStations(ctx context.Context, db *gorm.DB, q PageQuery, scopes ...operations.DataScope) (Page[Station], error) {
	out := Page[Station]{Items: []Station{}, Page: q.Page, PageSize: q.PageSize}
	query := db.WithContext(ctx).Model(&Station{}).Where("deleted_at IS NULL")
	if len(scopes) > 0 {
		query = ScopeStations(scopes[0], query, "id")
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := dbutil.LikePattern(q.Keyword)
		query = query.Where("(name LIKE ? ESCAPE '!' OR address LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// oneOfStatus 校验枚举字段是否落在空白分隔的白名单里；空值放行，表示不按该字段过滤。
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
