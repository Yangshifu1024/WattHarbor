package devices

// AlertStatus 是中央库 alert_event.status 列的告警状态，用于告警列表筛选与看板统计。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
//
// resolved 在当前 Go 代码中没有写入或判定分支，只出现在集成测试夹具中，
// 保留该取值以完整覆盖列约束，不得据此虚构流转规则。
type AlertStatus string

const (
	// AlertActive 表示告警已产生且未处理。
	AlertActive AlertStatus = "active"
	// AlertAcknowledged 表示运维已确认告警但尚未解决。
	AlertAcknowledged AlertStatus = "acknowledged"
	// AlertResolved 表示告警已人工解决。
	AlertResolved AlertStatus = "resolved"
	// AlertAutoResolved 表示告警条件消失后由系统自动解决。
	AlertAutoResolved AlertStatus = "auto_resolved"
)

// Known 判断是否属于该列的数据库合法集合（四值）。
func (s AlertStatus) Known() bool {
	switch s {
	case AlertActive, AlertAcknowledged, AlertResolved, AlertAutoResolved:
		return true
	default:
		return false
	}
}

// AllAlertStatuses 是 AlertStatus 的全量取值串，供读取筛选白名单直接引用。
// 顺序与上方常量声明一致。新增状态取值不会自动进入该白名单，需要按业务准入规则显式补充。
const AllAlertStatuses = "active acknowledged resolved auto_resolved"

// AlertSeverity 是中央库 alert_event.severity 列的告警级别。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（warning/critical/fatal）。
//
// warning 当前没有生产写入路径，仅作为数据库默认值存在；
// 设备协议适配器只产出 critical 与 fatal。保留该取值以完整覆盖列约束。
type AlertSeverity string

const (
	// AlertSeverityWarning 是警告级别；当前无生产写入路径，仅为数据库默认值。
	AlertSeverityWarning AlertSeverity = "warning"
	// AlertSeverityCritical 是严重级别。
	AlertSeverityCritical AlertSeverity = "critical"
	// AlertSeverityFatal 是致命级别。
	AlertSeverityFatal AlertSeverity = "fatal"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (s AlertSeverity) Known() bool {
	switch s {
	case AlertSeverityWarning, AlertSeverityCritical, AlertSeverityFatal:
		return true
	default:
		return false
	}
}

// AllAlertSeverities 是 AlertSeverity 的全量取值串，供写入准入白名单直接引用。
const AllAlertSeverities = "warning critical fatal"
