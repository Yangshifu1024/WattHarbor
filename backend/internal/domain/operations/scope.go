package operations

// ScopeType 是 admin_data_scope.scope_type 列的数据范围类型，约束可见性维度。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（station/vendor）。
type ScopeType string

const (
	// ScopeStation 表示按站点维度限制可见范围。
	ScopeStation ScopeType = "station"
	// ScopeVendor 表示按厂商维度限制可见范围。
	ScopeVendor ScopeType = "vendor"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
// 仅用于识别未知值；归并时遇到未知类型按原行为静默忽略，不得据此报错。
func (t ScopeType) Known() bool {
	return t == ScopeStation || t == ScopeVendor
}

// MaxScopeItems 是单个账号可配置的数据范围条数上限。
const MaxScopeItems = 500

// DataScope 将账号的站点、厂商范围项归并为内存集合。
// Unrestricted 为真时表示账号没有任何范围记录，不附加数据范围限制；角色权限仍需单独校验。
type DataScope struct {
	AdminUserID  uint64    // 账号 ID
	ScopeType    ScopeType // 范围类型：station 限站点 / vendor 限厂商
	ScopeID      uint64    // 该类型下的对象 ID，配合 ScopeType 定位一个站点或厂商
	StationIDs   []uint64  // 归并后的站点 ID 列表
	VendorIDs    []uint64  // 归并后的厂商 ID 列表
	Unrestricted bool      // 是否不受限：无任何范围记录即为真
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

// AllowsStation 检查站点 ID；未限制范围或 StationIDs 为空时返回 true。
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

// AllowsVendor 检查厂商 ID；未限制范围或 VendorIDs 为空时返回 true。
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
// 不受限的账号可以；受限账号须只有一个站点，且没有厂商范围。
func (s DataScope) CanManageStationDefault() bool {
	return s.Unrestricted || (len(s.StationIDs) == 1 && len(s.VendorIDs) == 0)
}

// ValidScopeType 只接受 station 和 vendor，空值返回 false。
func ValidScopeType(scopeType ScopeType) bool {
	return scopeType.Known()
}

// ValidScopeIDs 要求 1–MaxScopeItems 个非零 ID；不检查重复项。
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

// MaskSlice 调用 MaskRow 对列表中的每一行脱敏，原地修改并返回。
func (m FieldMask) MaskSlice(resource string, rows []map[string]any) []map[string]any {
	for i := range rows {
		m.MaskRow(resource, rows[i])
	}
	return rows
}
