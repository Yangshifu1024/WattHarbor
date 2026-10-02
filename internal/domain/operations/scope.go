package operations

// 数据范围的两种类型，分别约束站点与厂商维度的可见性。
const (
	ScopeStation = "station"
	ScopeVendor  = "vendor"
)

// MaxScopeItems 是单个账号可配置的数据范围条数上限。
const MaxScopeItems = 500

// DataScope 将账号的站点、厂商范围项归并为内存集合。
// Unrestricted 为真时表示账号没有任何范围记录，不附加数据范围限制；角色权限仍需单独校验。
type DataScope struct {
	AdminUserID  uint64   // 账号 ID
	ScopeType    string   // 范围类型：station 限站点 / vendor 限厂商
	ScopeID      uint64   // 该类型下的对象 ID，配合 ScopeType 定位一个站点或厂商
	StationIDs   []uint64 // 归并后的站点 ID 列表
	VendorIDs    []uint64 // 归并后的厂商 ID 列表
	Unrestricted bool     // 是否不受限：无任何范围记录即为真
}

// NewDataScope 由一组原始范围行归并出内存范围集合。空输入即不受限。
func NewDataScope(adminUserID uint64, rows []DataScope) DataScope {
	scope := DataScope{AdminUserID: adminUserID}
	if len(rows) == 0 {
		scope.Unrestricted = true
		return scope
	}
	for _, row := range rows {
		switch row.ScopeType {
		case ScopeStation:
			scope.StationIDs = append(scope.StationIDs, row.ScopeID)
		case ScopeVendor:
			scope.VendorIDs = append(scope.VendorIDs, row.ScopeID)
		}
	}
	return scope
}

// AllowsStation 判断站点是否在可见范围内，供详情接口执行归属校验。
func (s DataScope) AllowsStation(id uint64) bool {
	if s.Unrestricted || len(s.StationIDs) == 0 {
		return true
	}
	for _, allowed := range s.StationIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

// AllowsVendor 判断厂商是否在可见范围内。
func (s DataScope) AllowsVendor(id uint64) bool {
	if s.Unrestricted || len(s.VendorIDs) == 0 {
		return true
	}
	for _, allowed := range s.VendorIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

// CanManageStationDefault 判断该范围是否能改站点的计费默认配置。
// 不受限的账号可以；有站点范围限制时必须恰好只管一个站点。
func (s DataScope) CanManageStationDefault() bool {
	return s.Unrestricted || (len(s.StationIDs) == 1 && len(s.VendorIDs) == 0)
}

// ValidScopeType 判断范围类型是否为已知类型，空范围（未配置）也算合法。
func ValidScopeType(scopeType string) bool {
	return scopeType == ScopeStation || scopeType == ScopeVendor
}

// ValidScopeIDs 校验范围 ID 列表：空表示未配置，数量不得超过 MaxScopeItems，且不允许出现零值主键。
func ValidScopeIDs(ids []uint64) bool {
	if len(ids) == 0 || len(ids) > MaxScopeItems {
		return false
	}
	for _, id := range ids {
		if id == 0 {
			return false
		}
	}
	return true
}

// MaskPlaceholder 是被遮蔽字段的固定响应占位值。
const MaskPlaceholder = "***"

// FieldMask 描述按角色配置的响应字段遮蔽规则。
type FieldMask struct {
	RoleID uint64        // 规则所属角色 ID
	Fields []MaskedField // 逐条遮蔽规则
}

// MaskedField 是一条遮蔽规则：某资源下的某个字段需要打码。
type MaskedField struct {
	Resource string // 资源名，对应业务模块（如 user）
	Field    string // 字段名，需打码的列
}

// Hide 判断某资源的某个字段对该角色是否必须打码。
func (m FieldMask) Hide(resource, field string) bool {
	for _, rule := range m.Fields {
		if rule.Resource == resource && rule.Field == field {
			return true
		}
	}
	return false
}

// MaskRow 就地将已有字段中命中脱敏规则的值替换为占位符，不补充缺失字段。
func (m FieldMask) MaskRow(resource string, row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	for _, rule := range m.Fields {
		if rule.Resource != resource {
			continue
		}
		if _, present := row[rule.Field]; present {
			row[rule.Field] = MaskPlaceholder
		}
	}
	return row
}

// MaskSlice 对一个列表响应里的每一行套用 MaskRow，原地修改并返回。
func (m FieldMask) MaskSlice(resource string, rows []map[string]any) []map[string]any {
	for i := range rows {
		m.MaskRow(resource, rows[i])
	}
	return rows
}
