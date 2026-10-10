package device

// 本文件集中定义 device 包自有持久化列的状态词表类型。
// 全部类型都是 string 的自定义类型，不定义 MarshalJSON/UnmarshalJSON，JSON 编解码与裸 string 完全一致。
// 每个类型只覆盖一列的词表：同名 status 属于不同表时必须使用不同类型，不得互相赋值或比较。

// StationStatus 是 central_db.station.status 列的站点运营状态，用于站点启停与 C 端可见性过滤。
// 取值与 migrations/central_db/0001_init.sql:1209 的 ENUM 列一致（active／disabled／construction）。
//
// 词表边界：本类型只覆盖 station 表。与同样叫 status 但分属其他表的三列分立，不得合并：
//   - device_meta.status 是 devices.DeviceMetaStatus（enabled／disabled／retired／fault）；
//   - device_fault_report.status 是 FaultStatus（open／dispatched／fixed／closed）。
//
// 三者的 active、disabled 等字面量部分相同，但权威列与语义不同。
type StationStatus string

// 站点运营状态。
const (
	StationActive       StationStatus = "active"       // 营业中，C 端可检索并可发起充电
	StationDisabled     StationStatus = "disabled"     // 停用，保留资料但不参与运营
	StationConstruction StationStatus = "construction" // 建设中
)

// AllStationStatuses 是 StationStatus 的全量取值串，供读取筛选白名单直接引用。
// 顺序与上方常量声明一致；新增取值不会自动进入白名单，须按接口准入规则显式补充。
const AllStationStatuses = "active disabled construction"

// Known 判断是否属于该列的数据库合法集合（三值），仅用于识别未知值。
// 读取路径遇到未知值时保持原值透传，不得据此拒绝、替换或降级；写入准入由具体操作单独判断。
func (s StationStatus) Known() bool {
	switch s {
	case StationActive, StationDisabled, StationConstruction:
		return true
	default:
		return false
	}
}

// FaultType 是 central_db.device_fault_report.fault_type 列的报修故障分类，
// 取值与 migrations/central_db/0001_init.sql:421 的 ENUM 列一致
// （mechanical／electrical／communication／display／other）。
//
// 词表边界：本类型只覆盖故障分类。它与同一张表的 report_source（ReportSource）和
// status（FaultStatus）是三列三个词表：FaultStatus 的取值 open／dispatched／fixed／closed
// 与本类型的 other 等字面量不得混用，报修单状态不能当作故障类型赋值或比较。
type FaultType string

// 报修故障分类。
const (
	FaultTypeMechanical    FaultType = "mechanical"    // 机械故障
	FaultTypeElectrical    FaultType = "electrical"    // 电气故障
	FaultTypeCommunication FaultType = "communication" // 通信故障
	FaultTypeDisplay       FaultType = "display"       // 显示故障
	FaultTypeOther         FaultType = "other"         // 其他
)

// Known 判断是否属于该列的数据库合法集合（五值），仅用于识别未知值。
func (t FaultType) Known() bool {
	switch t {
	case FaultTypeMechanical, FaultTypeElectrical, FaultTypeCommunication, FaultTypeDisplay, FaultTypeOther:
		return true
	default:
		return false
	}
}

// ReportSource 是 central_db.device_fault_report.report_source 列的报修来源，
// 取值与 migrations/central_db/0001_init.sql:420 的 ENUM 列一致（user／inspect／monitor）。
//
// 词表边界：本类型只覆盖来源列。当前只有 user 有写入路径（internal/device/user_fault_repo.go 的
// InsertFaultReport）；inspect 与 monitor 无写入路径，保留常量以完整覆盖列约束。
// 与 FaultStatus 分立：open 是报修单状态，不是来源。
type ReportSource string

// 报修来源。
const (
	ReportSourceUser    ReportSource = "user"    // 用户提交，唯一有写入路径的来源
	ReportSourceInspect ReportSource = "inspect" // 巡检提交，当前无写入路径
	ReportSourceMonitor ReportSource = "monitor" // 监控上报，当前无写入路径
)

// Known 判断是否属于该列的数据库合法集合（三值），仅用于识别未知值。
func (r ReportSource) Known() bool {
	switch r {
	case ReportSourceUser, ReportSourceInspect, ReportSourceMonitor:
		return true
	default:
		return false
	}
}

// ImportStatus 是 central_db.device_import.status 列的导入任务状态，用于导入列表与重试判定。
// 取值与 migrations/central_db/0001_init.sql:1013 的 ENUM 列一致（pending／completed／failed）。
//
// 词表边界：本类型只覆盖导入任务表。device_import_identity 没有 status 列；
// 导入过程中创建 device_meta 记录时写入的 enabled 属于 devices.DeviceMetaStatus，不是本词表。
type ImportStatus string

// 导入任务状态。
const (
	ImportStatusPending   ImportStatus = "pending"   // 已受理待执行／上次失败可重试
	ImportStatusCompleted ImportStatus = "completed" // 开通完成，重放不再执行
	ImportStatusFailed    ImportStatus = "failed"    // 网关未确认，保留清单供重试
)

// Known 判断是否属于该列的数据库合法集合（三值），仅用于识别未知值。
// 读取路径遇到未知值时保持原值透传，不得据此判定为可重试或直接改写任务状态。
func (s ImportStatus) Known() bool {
	switch s {
	case ImportStatusPending, ImportStatusCompleted, ImportStatusFailed:
		return true
	default:
		return false
	}
}

// FaultEventType 是 central_db.device_fault_report_event.event_type 列的报修流转事件类型，
// 取值与 migrations/central_db/0001_init.sql:439 的 ENUM 列一致
// （reported／dispatched／reassigned／fixed／closed／migration_baseline）。
//
// 词表边界与写入集合：六值不是统一的可写集合，必须按操作区分，参见 Known 与 BackgroundWritable。
// 与 FaultStatus 分立：open 不在本列内；reassigned 与 migration_baseline 也不在 FaultStatus 内。
// 本列的 from_status／to_status 是 varchar(24) 而非 ENUM，沿用 FaultStatus 表示并保留空串，
// 不因本类型存在就新增拒绝或把空串转成 null。
type FaultEventType string

// 报修流转事件类型。
const (
	FaultEventReported          FaultEventType = "reported"           // 用户提交报修时写入的首条事件
	FaultEventDispatched        FaultEventType = "dispatched"         // 首次指派处理人
	FaultEventReassigned        FaultEventType = "reassigned"         // 改派其他处理人
	FaultEventFixed             FaultEventType = "fixed"              // 处理人提交修复
	FaultEventClosed            FaultEventType = "closed"             // 关闭报修单
	FaultEventMigrationBaseline FaultEventType = "migration_baseline" // 当前无写入路径，保留为兼容读取
)

// Known 判断是否属于该列的数据库合法集合（六值），仅用于识别未知值。
// 读取路径遇到未知值时保持原值透传，不得据此过滤、替换或降级历史事件。
func (e FaultEventType) Known() bool {
	switch e {
	case FaultEventReported, FaultEventDispatched, FaultEventReassigned, FaultEventFixed, FaultEventClosed, FaultEventMigrationBaseline:
		return true
	default:
		return false
	}
}

// BackgroundWritable 判断是否属于后台工单处理操作写入的集合（四值）。
// 该集合只是 Dispatch／Resolve 两条用例的可写范围，不是数据库合法集合：
// 用户报修入口写入 reported（见 InsertFaultReport），migration_baseline 无写入路径。
// 因此本方法不构成全局写入校验，也不限制仓储层 AppendFaultEvent 的取值。
func (e FaultEventType) BackgroundWritable() bool {
	switch e {
	case FaultEventDispatched, FaultEventReassigned, FaultEventFixed, FaultEventClosed:
		return true
	default:
		return false
	}
}
