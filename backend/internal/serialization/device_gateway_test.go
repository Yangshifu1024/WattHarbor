//go:build contract

package serialization

import (
	"testing"

	"chargepilot/internal/contracts/gateway"
	"chargepilot/internal/device"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/money"
	gatewayrepo "chargepilot/internal/gateway/repo"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/protocol"
	workerrepo "chargepilot/internal/worker/repo"
)

// 本文件覆盖 B 组（设备、网关、协议）枚举的对外 JSON 形状。
//
// 三类载体，按「离对外响应有多远」区分，断言强度不同：
//
//  1. 真正的带 JSON tag 的对外响应/契约结构体（device.Device、device.Station、
//     gateway.VendorReply、gateway.StopRequest、gateway.EvidenceSample 等）。
//     断言同时锁死 tag 名与取值。
//  2. 真实但**没有 JSON tag** 的结构体，其 Go 字段名就是它的字节形状：
//     device.DeviceAlert（告警）、device.FaultEventInput（报修事件）、
//     protocol.Event（设备事件，持久化原样存 payload）、protocol.SessionRecord
//     （会话审计）、workerrepo.ExecutionLog（执行日志）。这些字段名同样是契约。
//  3. **没有任何结构体承载**的枚举（devices.GatewayDeviceStatus、
//     devices.AlertSeverity、gatewayrepo.CardDeliveryStatus、device.ReportSource）：
//     只序列化类型自身，仍然锁死「该类型的每个取值序列化后就是那个字面量、
//     且没有 MarshalJSON 改写它」。这三类差异见各用例注释。
//
// 不修改生产代码：下面记录的是当前输出，不是「应该的形状」。

// TestDeviceMetaStatusAllValues 锁定 devices.DeviceMetaStatus 的全部 4 个取值，
// 载体是真实的 device.Device（后台设备列表与详情）。
//
// enabled／disabled／retired 三个字面量与 GatewayDeviceStatus 完全相同，
// 本类型独有的第四值是 fault，断言它落到 "status" 字段而不是被吞掉。
func TestDeviceMetaStatusAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.Device
		Want  string
	}{
		{"enabled", device.Device{Status: devices.DeviceEnabled}, `{"id":0,"device_id":"","station_id":null,"station_name":null,"vendor_id":null,"vendor_name":null,"last_heartbeat_at":null,"signal_strength":null,"signal_at":null,"ports":null,"runtime_available":false,"model":null,"serial_no":null,"status":"enabled","warranty_until":null,"tags":null,"updated_at":"0001-01-01T00:00:00Z","protocol_adapter":"","charge_mode":"","reports_energy":false,"reports_segmented_power":false}`},
		{"disabled", device.Device{Status: devices.DeviceDisabled}, `{"id":0,"device_id":"","station_id":null,"station_name":null,"vendor_id":null,"vendor_name":null,"last_heartbeat_at":null,"signal_strength":null,"signal_at":null,"ports":null,"runtime_available":false,"model":null,"serial_no":null,"status":"disabled","warranty_until":null,"tags":null,"updated_at":"0001-01-01T00:00:00Z","protocol_adapter":"","charge_mode":"","reports_energy":false,"reports_segmented_power":false}`},
		{"retired", device.Device{Status: devices.DeviceRetired}, `{"id":0,"device_id":"","station_id":null,"station_name":null,"vendor_id":null,"vendor_name":null,"last_heartbeat_at":null,"signal_strength":null,"signal_at":null,"ports":null,"runtime_available":false,"model":null,"serial_no":null,"status":"retired","warranty_until":null,"tags":null,"updated_at":"0001-01-01T00:00:00Z","protocol_adapter":"","charge_mode":"","reports_energy":false,"reports_segmented_power":false}`},
		{"fault_本类型独有_网关设备状态没有该值", device.Device{Status: devices.DeviceFault}, `{"id":0,"device_id":"","station_id":null,"station_name":null,"vendor_id":null,"vendor_name":null,"last_heartbeat_at":null,"signal_strength":null,"signal_at":null,"ports":null,"runtime_available":false,"model":null,"serial_no":null,"status":"fault","warranty_until":null,"tags":null,"updated_at":"0001-01-01T00:00:00Z","protocol_adapter":"","charge_mode":"","reports_energy":false,"reports_segmented_power":false}`},
		{"零值_空串而不是null", device.Device{}, `{"id":0,"device_id":"","station_id":null,"station_name":null,"vendor_id":null,"vendor_name":null,"last_heartbeat_at":null,"signal_strength":null,"signal_at":null,"ports":null,"runtime_available":false,"model":null,"serial_no":null,"status":"","warranty_until":null,"tags":null,"updated_at":"0001-01-01T00:00:00Z","protocol_adapter":"","charge_mode":"","reports_energy":false,"reports_segmented_power":false}`},
	})
}

// TestGatewayDeviceStatusAllValues 锁定 devices.GatewayDeviceStatus 的全部 3 个取值。
//
// **这个枚举没有任何对外结构体承载**：gateway/repo 里它只出现在
// backfill_repo.go 的匿名扫描 struct（无 JSON tag，且不参与任何响应），
// 因此序列化后暴露的是类型自身的字面量。
// 断言的目的是锁死「它不会因为未来被塞进某个响应结构体而改变字面量」，
// 同时与上一条 TestDeviceMetaStatusAllValues 的 fault 形成对照。
func TestGatewayDeviceStatusAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value devices.GatewayDeviceStatus
		Want  string
	}{
		{"enabled_与_DeviceMetaStatus_字面量相同", devices.GatewayDeviceEnabled, `"enabled"`},
		{"disabled_与_DeviceMetaStatus_字面量相同", devices.GatewayDeviceDisabled, `"disabled"`},
		{"retired_与_DeviceMetaStatus_字面量相同", devices.GatewayDeviceRetired, `"retired"`},
	})
}

// TestDeviceMetaStatusVersusGatewayDeviceStatus 锁死两个「设备状态」词表的边界。
//
// 本轮改造最核心的风险：两者前三值字面量逐字节相同，只有 DeviceMetaStatus
// 有 fault。任何一个 struct 字段被换成另一个类型，编译能过、JSON 也仍能输出，
// 只有这条对照能发现「网关库 device.status 被当成了中央 device_meta.status」。
func TestDeviceMetaStatusVersusGatewayDeviceStatus(t *testing.T) {
	// 同一字面量在两个类型下序列化结果相同——这正是它们容易被混用的原因。
	assertEach(t, []struct {
		Name  string
		Value any
		Want  string
	}{
		{"central_enabled", devices.DeviceEnabled, `"enabled"`},
		{"gateway_enabled", devices.GatewayDeviceEnabled, `"enabled"`},
		{"central_disabled", devices.DeviceDisabled, `"disabled"`},
		{"gateway_disabled", devices.GatewayDeviceDisabled, `"disabled"`},
		{"central_retired", devices.DeviceRetired, `"retired"`},
		{"gateway_retired", devices.GatewayDeviceRetired, `"retired"`},
		{"central_fault_网关库该列无此值", devices.DeviceFault, `"fault"`},
	})

	// 在真实响应结构体里，四值与三值落在同一个 "status" 字段名上。
	assertJSON(t, device.Device{Status: devices.DeviceFault}, `{"id":0,"device_id":"","station_id":null,"station_name":null,"vendor_id":null,"vendor_name":null,"last_heartbeat_at":null,"signal_strength":null,"signal_at":null,"ports":null,"runtime_available":false,"model":null,"serial_no":null,"status":"fault","warranty_until":null,"tags":null,"updated_at":"0001-01-01T00:00:00Z","protocol_adapter":"","charge_mode":"","reports_energy":false,"reports_segmented_power":false}`)
}

// TestAlertStatusAllValues 锁定 devices.AlertStatus 的全部 4 个取值，
// 载体是真实的 device.DeviceAlert（告警状态、最新故障键与分区月份）。
//
// 该结构体没有 JSON tag，Go 字段名就是它的字节形状：注意键是 "Status"
// 而非 "status"。后台告警列表接口返回的是 []map[string]any，不经本结构体，
// 所以这里的断言锁的是类型形状，不是该接口的线上形状。
func TestAlertStatusAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.DeviceAlert
		Want  string
	}{
		{"active", device.DeviceAlert{Status: devices.AlertActive}, `{"ID":0,"DeviceID":"","Metric":"","EventID":"","Status":"active","CreatedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","ResolvedAt":null}`},
		{"acknowledged", device.DeviceAlert{Status: devices.AlertAcknowledged}, `{"ID":0,"DeviceID":"","Metric":"","EventID":"","Status":"acknowledged","CreatedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","ResolvedAt":null}`},
		{"resolved_当前无生产写入路径", device.DeviceAlert{Status: devices.AlertResolved}, `{"ID":0,"DeviceID":"","Metric":"","EventID":"","Status":"resolved","CreatedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","ResolvedAt":null}`},
		{"auto_resolved", device.DeviceAlert{Status: devices.AlertAutoResolved}, `{"ID":0,"DeviceID":"","Metric":"","EventID":"","Status":"auto_resolved","CreatedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","ResolvedAt":null}`},
		{"零值_空串", device.DeviceAlert{}, `{"ID":0,"DeviceID":"","Metric":"","EventID":"","Status":"","CreatedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","ResolvedAt":null}`},
	})
}

// TestAlertSeverityAllValues 锁定 devices.AlertSeverity 的全部 3 个取值。
//
// **这个枚举没有任何对外结构体承载**：它只出现在 protocol/dc589/fault.go 的
// FaultMetric 返回值，随后作为 map[string]any 的元素写入 alert_event 表。
// 因此只能序列化类型自身，锁死字面量与「warning 无生产写入路径」这一事实。
func TestAlertSeverityAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value devices.AlertSeverity
		Want  string
	}{
		{"warning_仅数据库默认值", devices.AlertSeverityWarning, `"warning"`},
		{"critical", devices.AlertSeverityCritical, `"critical"`},
		{"fatal", devices.AlertSeverityFatal, `"fatal"`},
	})
}

// TestStationStatusAllValues 锁定 device.StationStatus 的全部 3 个取值，
// 载体分别是真实的 device.Station（站点资料对外模型）与 device.StationInput
// （站点创建/更新的请求体）——两者必须给出相同的字节。
//
// station.status 的 "disabled" 与 device_meta.status、vendor.status、
// gateway device.status 的字面量相同，权威列不同。
func TestStationStatusAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.Station
		Want  string
	}{
		{"active", device.Station{Status: device.StationActive}, `{"id":0,"name":"","address":null,"longitude":0,"latitude":0,"status":"active","contact_phone":null}`},
		{"disabled", device.Station{Status: device.StationDisabled}, `{"id":0,"name":"","address":null,"longitude":0,"latitude":0,"status":"disabled","contact_phone":null}`},
		{"construction", device.Station{Status: device.StationConstruction}, `{"id":0,"name":"","address":null,"longitude":0,"latitude":0,"status":"construction","contact_phone":null}`},
		{"零值_空串", device.Station{}, `{"id":0,"name":"","address":null,"longitude":0,"latitude":0,"status":"","contact_phone":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value device.StationInput
		Want  string
	}{
		{"请求体_active", device.StationInput{Status: device.StationActive}, `{"code":null,"name":"","address":null,"longitude":null,"latitude":null,"status":"active","contact_phone":null}`},
		{"请求体_disabled", device.StationInput{Status: device.StationDisabled}, `{"code":null,"name":"","address":null,"longitude":null,"latitude":null,"status":"disabled","contact_phone":null}`},
		{"请求体_construction", device.StationInput{Status: device.StationConstruction}, `{"code":null,"name":"","address":null,"longitude":null,"latitude":null,"status":"construction","contact_phone":null}`},
	})
}

// TestImportStatusAllValues 锁定 device.ImportStatus 的全部 3 个取值，
// 载体是真实的 device.ImportJob（后台设备导入任务列表返回记录）。
//
// "pending" 与 charge_command.status、card_event_delivery.status 同字面量，
// 但导入任务的 pending／completed／failed 与之无关。
func TestImportStatusAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.ImportJob
		Want  string
	}{
		{"pending", device.ImportJob{Status: device.ImportStatusPending}, `{"import_id":"","status":"pending","last_error":null,"actor_id":0,"attempts":0}`},
		{"completed", device.ImportJob{Status: device.ImportStatusCompleted}, `{"import_id":"","status":"completed","last_error":null,"actor_id":0,"attempts":0}`},
		{"failed", device.ImportJob{Status: device.ImportStatusFailed}, `{"import_id":"","status":"failed","last_error":null,"actor_id":0,"attempts":0}`},
		{"零值_空串", device.ImportJob{}, `{"import_id":"","status":"","last_error":null,"actor_id":0,"attempts":0}`},
	})
}

// TestFaultTypeAllValues 锁定 device.FaultType 的全部 5 个取值，
// 载体是真实的 device.FaultSubmission（C 端用户报障请求体）。
//
// 该结构体的 Images 无 omitempty，零值序列化为 null 而不是 []；
// 这一形态与 wire.RefundView 的空切片输出 [] 不同，属不同结构体的不同取舍。
func TestFaultTypeAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.FaultSubmission
		Want  string
	}{
		{"mechanical", device.FaultSubmission{FaultType: device.FaultTypeMechanical}, `{"device_id":"","port_no":0,"fault_type":"mechanical","description":null,"images":null}`},
		{"electrical", device.FaultSubmission{FaultType: device.FaultTypeElectrical}, `{"device_id":"","port_no":0,"fault_type":"electrical","description":null,"images":null}`},
		{"communication", device.FaultSubmission{FaultType: device.FaultTypeCommunication}, `{"device_id":"","port_no":0,"fault_type":"communication","description":null,"images":null}`},
		{"display", device.FaultSubmission{FaultType: device.FaultTypeDisplay}, `{"device_id":"","port_no":0,"fault_type":"display","description":null,"images":null}`},
		{"other", device.FaultSubmission{FaultType: device.FaultTypeOther}, `{"device_id":"","port_no":0,"fault_type":"other","description":null,"images":null}`},
		{"零值_空串", device.FaultSubmission{}, `{"device_id":"","port_no":0,"fault_type":"","description":null,"images":null}`},
	})
}

// TestReportSourceAllValues 锁定 device.ReportSource 的全部 3 个取值。
//
// **这个枚举没有任何对外结构体承载**：user_fault_repo.go 的 InsertFaultReport
// 把它写进 map[string]any 落 device_fault_report.report_source 列，读侧
// ListUserFaultReports 也不选该列。只能序列化类型自身。
func TestReportSourceAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.ReportSource
		Want  string
	}{
		{"user_唯一有写入路径的来源", device.ReportSourceUser, `"user"`},
		{"inspect_当前无写入路径", device.ReportSourceInspect, `"inspect"`},
		{"monitor_当前无写入路径", device.ReportSourceMonitor, `"monitor"`},
	})
}

// TestFaultEventTypeAllValues 锁定 device.FaultEventType 的全部 6 个取值，
// 载体是真实的 device.FaultEventInput（报修流转事件落库输入）。
//
// 与 device.FaultStatus 分立：dispatched／fixed／closed 三值字面量与报修单
// 状态重合，reassigned 与 migration_baseline 则只存在于事件列。
// FromStatus 零值序列化为 ""，不是 null——这是 from_status 列保留空串的写法的镜像。
func TestFaultEventTypeAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value device.FaultEventInput
		Want  string
	}{
		{"reported", device.FaultEventInput{EventType: device.FaultEventReported}, `{"ReportID":0,"ActorID":0,"EventType":"reported","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
		{"dispatched", device.FaultEventInput{EventType: device.FaultEventDispatched}, `{"ReportID":0,"ActorID":0,"EventType":"dispatched","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
		{"reassigned_不在_FaultStatus_内", device.FaultEventInput{EventType: device.FaultEventReassigned}, `{"ReportID":0,"ActorID":0,"EventType":"reassigned","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
		{"fixed", device.FaultEventInput{EventType: device.FaultEventFixed}, `{"ReportID":0,"ActorID":0,"EventType":"fixed","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
		{"closed", device.FaultEventInput{EventType: device.FaultEventClosed}, `{"ReportID":0,"ActorID":0,"EventType":"closed","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
		{"migration_baseline_不在_FaultStatus_内_当前无写入路径", device.FaultEventInput{EventType: device.FaultEventMigrationBaseline}, `{"ReportID":0,"ActorID":0,"EventType":"migration_baseline","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
		{"零值_空串", device.FaultEventInput{}, `{"ReportID":0,"ActorID":0,"EventType":"","FromStatus":"","ToStatus":"","AssignedTo":0,"Note":null,"UserVisible":false}`},
	})
}

// TestChargeCommandStatusVersusStopCommandStatus 锁死启动与停机两个命令状态的边界。
//
// 启动命令 5 值含 stopping，停机命令 4 值不含；两者的 pending／sent／acked／rejected
// 字面量逐字节相同。把启动的 stopping 判定扩散到停机路径，
// 或者把两个词表取并集，都会在这里暴露。
//
// 载体是真实的 contracts/gateway.StartReply 与 StopReply，并补上
// SyncCommand（worker 读取启动命令）与 CommandReply 的 omitempty 边界。
func TestChargeCommandStatusVersusStopCommandStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.SyncCommand
		Want  string
	}{
		{"启动命令_pending", gateway.SyncCommand{Status: devices.CommandPending}, `{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"pending","result_code":null,"ack_at":null}`},
		{"启动命令_sent", gateway.SyncCommand{Status: devices.CommandSent}, `{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"sent","result_code":null,"ack_at":null}`},
		{"启动命令_acked", gateway.SyncCommand{Status: devices.CommandAcked}, `{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"acked","result_code":null,"ack_at":null}`},
		{"启动命令_stopping_只属于启动命令", gateway.SyncCommand{Status: devices.CommandStopping}, `{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"stopping","result_code":null,"ack_at":null}`},
		{"启动命令_rejected", gateway.SyncCommand{Status: devices.CommandRejected}, `{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"rejected","result_code":null,"ack_at":null}`},
		{"零值_空串", gateway.SyncCommand{}, `{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"","result_code":null,"ack_at":null}`},
	})

	assertEach(t, []struct {
		Name  string
		Value gateway.StopReply
		Want  string
	}{
		{"停机命令_pending", gateway.StopReply{Status: devices.StopCommandPending}, `{"accepted":false,"stopped":false,"command_id":"","status":"pending"}`},
		{"停机命令_sent", gateway.StopReply{Status: devices.StopCommandSent}, `{"accepted":false,"stopped":false,"command_id":"","status":"sent"}`},
		{"停机命令_acked", gateway.StopReply{Status: devices.StopCommandAcked}, `{"accepted":false,"stopped":false,"command_id":"","status":"acked"}`},
		{"停机命令_rejected", gateway.StopReply{Status: devices.StopCommandRejected}, `{"accepted":false,"stopped":false,"command_id":"","status":"rejected"}`},
		{"零值_空串_停机命令无_stopping", gateway.StopReply{}, `{"accepted":false,"stopped":false,"command_id":"","status":""}`},
	})

	// 停机流程只有这四个取值；stopping 不在其中，因此 StopReply 永远不输出它。
	assertEach(t, []struct {
		Name  string
		Value any
		Want  string
	}{
		{"charge_command_stopping", devices.CommandStopping, `"stopping"`},
		{"stop_command_无_stopping_常量", devices.StopCommandStatus("stopping"), `"stopping"`},
	})
}

// TestCommandReplyOmitEmptyCommand 锁定 gateway.CommandReply 的 omitempty 边界：
// 未找到命令时整段 command 省略（而不是 null），找到时输出完整 SyncCommand。
func TestCommandReplyOmitEmptyCommand(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.CommandReply
		Want  string
	}{
		{"未找到_command整段省略", gateway.CommandReply{}, `{"found":false}`},
		{"找到_输出完整命令", gateway.CommandReply{Found: true, Command: &gateway.SyncCommand{Status: devices.CommandAcked}}, `{"found":true,"command":{"command_id":"","stop_command_id":"","charge_order_id":0,"order_no":"","device_id":"","port_no":0,"port_id":null,"status":"acked","result_code":null,"ack_at":null}}`},
	})
}

// TestCardPortReplyOmitEmptyPortCode 锁定 gateway.CardPortReply 的 omitempty 边界：
// 未开通端口时 port_code 省略（不是 ""），开通时输出业务码。
func TestCardPortReplyOmitEmptyPortCode(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.CardPortReply
		Want  string
	}{
		{"未开通_port_code省略", gateway.CardPortReply{}, `{"found":false}`},
		{"已开通_输出业务码", gateway.CardPortReply{Found: true, PortCode: "P1"}, `{"found":true,"port_code":"P1"}`},
		{"已开通但空业务码_仍省略", gateway.CardPortReply{Found: true}, `{"found":true}`},
	})
}

// TestStopSourceAllValues 锁定 gateway.StopSource 的全部 3 个取值，
// 载体是真实的 gateway.StopRequest。
//
// "admin" 是 oneof 标签接受的合法取值但当前无生产方，因此没有常量；
// 这里直接用字面量转换构造，锁住它必须序列化为 "admin" 而不是被拒或改名。
func TestStopSourceAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.StopRequest
		Want  string
	}{
		{"user_app", gateway.StopRequest{Source: gateway.StopSourceUser}, `{"order_no":"","user_id":0,"source":"user_app"}`},
		{"admin_oneof接受但无常量", gateway.StopRequest{Source: gateway.StopSource("admin")}, `{"order_no":"","user_id":0,"source":"admin"}`},
		{"auto", gateway.StopRequest{Source: gateway.StopSourceAuto}, `{"order_no":"","user_id":0,"source":"auto"}`},
		{"零值_空串", gateway.StopRequest{}, `{"order_no":"","user_id":0,"source":""}`},
	})
}

// TestCardDeliveryStatusVersusCardAdvanceOutcome 锁死「刷卡投递」两个词表的边界。
//
// gatewayrepo.CardDeliveryStatus 是库内 card_event_delivery.status 的投递进度
// （pending／done），gateway.CardAdvanceOutcome 是 worker 上报推进动作的契约输入
// （retry／done）：两者只有 done 同值，另一端一个是 pending、一个是 retry。
// 误用会让 worker 的 retry 被写成库内 pending，或反过来。
//
// CardDeliveryStatus 没有任何对外结构体承载（cardDeliveryRow 只映射 event_key，
// status 列不进任何响应），因此该侧序列化类型自身。
func TestCardDeliveryStatusVersusCardAdvanceOutcome(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gatewayrepo.CardDeliveryStatus
		Want  string
	}{
		{"库内投递状态_pending", gatewayrepo.CardDeliveryPending, `"pending"`},
		{"库内投递状态_done", gatewayrepo.CardDeliveryDone, `"done"`},
	})

	assertEach(t, []struct {
		Name  string
		Value gateway.CardAdvanceRequest
		Want  string
	}{
		{"契约输入_retry_库内无此值", gateway.CardAdvanceRequest{Outcome: gateway.CardAdvanceOutcomeRetry}, `{"event_key":"","outcome":"retry","error":""}`},
		{"契约输入_done_两侧同值", gateway.CardAdvanceRequest{Outcome: gateway.CardAdvanceOutcomeDone}, `{"event_key":"","outcome":"done","error":""}`},
		{"零值_空串", gateway.CardAdvanceRequest{}, `{"event_key":"","outcome":"","error":""}`},
	})

	// 明确对照：库内那侧的另一半是 pending，契约那侧的另一半是 retry。
	assertEach(t, []struct {
		Name  string
		Value any
		Want  string
	}{
		{"库内不是retry", gatewayrepo.CardDeliveryStatus("retry"), `"retry"`},
		{"契约不是pending", gateway.CardAdvanceOutcome("pending"), `"pending"`},
	})
}

// TestCardAdvanceErrorIsNotOmitEmpty 锁定 CardAdvanceRequest.Error 不是 omitempty：
// 空重试原因仍然输出 "error":""，不能靠字段是否存在判断有无原因。
func TestCardAdvanceErrorIsNotOmitEmpty(t *testing.T) {
	assertJSON(t, gateway.CardAdvanceRequest{Outcome: gateway.CardAdvanceOutcomeRetry}, `{"event_key":"","outcome":"retry","error":""}`)
	assertJSON(t, gateway.CardAdvanceRequest{Outcome: gateway.CardAdvanceOutcomeRetry, Error: "gateway unavailable"}, `{"event_key":"","outcome":"retry","error":"gateway unavailable"}`)
}

// TestAdapterIDAllValues 锁定 protocol.AdapterID 的全部 1 个取值，
// 载体分别是真实的 gateway.VendorReply（adapter_class）与 protocol.Event（Protocol）。
//
// AdapterID 描述「这台设备说的是哪种协议」，只有 dc589；
// 与 VendorProtocol（tcp/mqtt/hybrid）和 Transport（tcp/mqtt）不是同一层级。
func TestAdapterIDAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.VendorReply
		Want  string
	}{
		{"dc589", gateway.VendorReply{AdapterClass: protocol.AdapterIDDC589}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"dc589","protocol":"","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"零值_空串_未知适配器按原值透传", gateway.VendorReply{AdapterClass: protocol.AdapterID("vendor-x")}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"vendor-x","protocol":"","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
	})
}

// TestVendorProtocolVersusTransport 锁死「厂商接入协议」与「单条会话传输方式」的边界。
//
// VendorProtocol 是厂商库 vendor.protocol（tcp／mqtt／hybrid），
// Transport 是 device_session.protocol（tcp／mqtt）：hybrid 不表示某条会话
// 正在同时跑两种协议，Transport.Known() 对 "hybrid" 返回 false。
//
// 载体分别是真实的 gateway.VendorReply 与 protocol.SessionRecord。
func TestVendorProtocolVersusTransport(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.VendorReply
		Want  string
	}{
		{"厂商协议_tcp", gateway.VendorReply{Protocol: devices.VendorProtocolTCP}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"tcp","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"厂商协议_mqtt", gateway.VendorReply{Protocol: devices.VendorProtocolMQTT}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"mqtt","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
		{"厂商协议_hybrid_会话传输无此值", gateway.VendorReply{Protocol: devices.VendorProtocolHybrid}, `{"id":0,"vendor_code":"","vendor_name":"","adapter_class":"","protocol":"hybrid","status":"","enabled_at":null,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}`},
	})

	assertEach(t, []struct {
		Name  string
		Value protocol.SessionRecord
		Want  string
	}{
		{"会话传输_tcp", protocol.SessionRecord{Transport: protocol.TransportTCP}, `{"SessionID":"","DeviceID":"","Transport":"tcp","RemoteAddr":"","StartedAt":"0001-01-01T00:00:00Z","LastActive":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","CloseReason":"","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"会话传输_mqtt", protocol.SessionRecord{Transport: protocol.TransportMQTT}, `{"SessionID":"","DeviceID":"","Transport":"mqtt","RemoteAddr":"","StartedAt":"0001-01-01T00:00:00Z","LastActive":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","CloseReason":"","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"零值_空串", protocol.SessionRecord{}, `{"SessionID":"","DeviceID":"","Transport":"","RemoteAddr":"","StartedAt":"0001-01-01T00:00:00Z","LastActive":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","CloseReason":"","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
	})

	// 三个「协议」词表并排：AdapterID 唯一值 dc589，VendorProtocol 三值，
	// Transport 两值。任何一个被合并都会在这里出现重复或不存在的取值。
	assertEach(t, []struct {
		Name  string
		Value any
		Want  string
	}{
		{"AdapterID_dc589", protocol.AdapterIDDC589, `"dc589"`},
		{"VendorProtocol_tcp", devices.VendorProtocolTCP, `"tcp"`},
		{"Transport_tcp_与_VendorProtocol_同字面量", protocol.TransportTCP, `"tcp"`},
		{"VendorProtocol_mqtt", devices.VendorProtocolMQTT, `"mqtt"`},
		{"Transport_mqtt_与_VendorProtocol_同字面量", protocol.TransportMQTT, `"mqtt"`},
		{"VendorProtocol_hybrid_Transport_无", devices.VendorProtocolHybrid, `"hybrid"`},
		{"Transport_不是_dc589", protocol.Transport("dc589"), `"dc589"`},
		{"AdapterID_不是_tcp", protocol.AdapterID("tcp"), `"tcp"`},
	})
}

// TestEventTypeAllValues 锁定 protocol.EventType 的全部 11 个取值，
// 载体是真实的 gateway.EvidenceSample（心跳证据页样本）。
//
// 注意该契约的 tag 是大写 "Type"／"DeviceID"／"ChargingPorts"：
// 这些是历史证据页 JSON 的既有键名，改成小写会打断在途窗口判定。
func TestEventTypeAllValues(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value gateway.EvidenceSample
		Want  string
	}{
		{"heartbeat", gateway.EvidenceSample{Type: protocol.Heartbeat}, `{"Type":"heartbeat","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"start_result", gateway.EvidenceSample{Type: protocol.StartResult}, `{"Type":"start_result","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"stop_result", gateway.EvidenceSample{Type: protocol.StopResult}, `{"Type":"stop_result","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"charge_end", gateway.EvidenceSample{Type: protocol.ChargeEnd}, `{"Type":"charge_end","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"fault", gateway.EvidenceSample{Type: protocol.Fault}, `{"Type":"fault","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"remote_result", gateway.EvidenceSample{Type: protocol.RemoteResult}, `{"Type":"remote_result","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"config_result", gateway.EvidenceSample{Type: protocol.ConfigResult}, `{"Type":"config_result","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"telemetry", gateway.EvidenceSample{Type: protocol.Telemetry}, `{"Type":"telemetry","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"time_sync", gateway.EvidenceSample{Type: protocol.TimeSync}, `{"Type":"time_sync","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"card_swipe", gateway.EvidenceSample{Type: protocol.CardSwipe}, `{"Type":"card_swipe","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"card_balance_query", gateway.EvidenceSample{Type: protocol.CardBalanceQuery}, `{"Type":"card_balance_query","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
		{"零值_空串", gateway.EvidenceSample{}, `{"Type":"","DeviceID":"","ReceivedAt":"0001-01-01T00:00:00Z","ChargingPorts":null}`},
	})
}

// TestProtocolEventPersistedShape 锁定 protocol.Event 的完整字节形状。
//
// 该结构体没有 JSON tag，且 device_event.payload 保存的就是它的原始 JSON
// （EnergyMilliKWh 字段名是历史契约，不能改名），因此 Go 字段名即持久化形状。
// EventType 与 AdapterID 在这里各出现一次，确保它们在持久化载荷里也
// 保持同样的字面量，不因另一处加了 MarshalJSON 而分叉。
func TestProtocolEventPersistedShape(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value protocol.Event
		Want  string
	}{
		{"heartbeat_含AdapterID", protocol.Event{Protocol: protocol.AdapterIDDC589, Type: protocol.Heartbeat}, `{"EventID":"","CardNumber":0,"Protocol":"dc589","DeviceID":"","Type":"heartbeat","Port":0,"ResultCode":0,"FaultCode":0,"EnergyMilliKWh":0,"PowerDeciWatts":0,"StopReason":0,"ConsumerType":0,"ReceivedAt":"0001-01-01T00:00:00Z","RawPayload":null,"SessionID":[0,0,0,0,0,0],"OrderNumber":"","ChargedSeconds":0,"StartedAt":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","VoltageV":0,"TemperatureC":0,"DeviceStatus":0,"Signal":0,"PortStates":null,"ChargingPorts":null}`},
		{"charge_end", protocol.Event{Type: protocol.ChargeEnd}, `{"EventID":"","CardNumber":0,"Protocol":"","DeviceID":"","Type":"charge_end","Port":0,"ResultCode":0,"FaultCode":0,"EnergyMilliKWh":0,"PowerDeciWatts":0,"StopReason":0,"ConsumerType":0,"ReceivedAt":"0001-01-01T00:00:00Z","RawPayload":null,"SessionID":[0,0,0,0,0,0],"OrderNumber":"","ChargedSeconds":0,"StartedAt":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","VoltageV":0,"TemperatureC":0,"DeviceStatus":0,"Signal":0,"PortStates":null,"ChargingPorts":null}`},
		{"card_swipe", protocol.Event{Type: protocol.CardSwipe}, `{"EventID":"","CardNumber":0,"Protocol":"","DeviceID":"","Type":"card_swipe","Port":0,"ResultCode":0,"FaultCode":0,"EnergyMilliKWh":0,"PowerDeciWatts":0,"StopReason":0,"ConsumerType":0,"ReceivedAt":"0001-01-01T00:00:00Z","RawPayload":null,"SessionID":[0,0,0,0,0,0],"OrderNumber":"","ChargedSeconds":0,"StartedAt":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","VoltageV":0,"TemperatureC":0,"DeviceStatus":0,"Signal":0,"PortStates":null,"ChargingPorts":null}`},
		{"零值_空串", protocol.Event{}, `{"EventID":"","CardNumber":0,"Protocol":"","DeviceID":"","Type":"","Port":0,"ResultCode":0,"FaultCode":0,"EnergyMilliKWh":0,"PowerDeciWatts":0,"StopReason":0,"ConsumerType":0,"ReceivedAt":"0001-01-01T00:00:00Z","RawPayload":null,"SessionID":[0,0,0,0,0,0],"OrderNumber":"","ChargedSeconds":0,"StartedAt":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","VoltageV":0,"TemperatureC":0,"DeviceStatus":0,"Signal":0,"PortStates":null,"ChargingPorts":null}`},
	})
}

// TestCloseReasonAllValues 锁定 protocol.CloseReason 的全部 8 个取值，
// 载体是真实的 protocol.SessionRecord（连接结束审计记录）。
//
// abandoned 由网关侧补记，不来自适配器；close_reason 是 VARCHAR 开放集合，
// 未知值必须原值透传，所以额外锁一条未知取值不被改写。
func TestCloseReasonAllValues(t *testing.T) {
	const shell = `{"SessionID":"","DeviceID":"","Transport":"","RemoteAddr":"","StartedAt":"0001-01-01T00:00:00Z","LastActive":"0001-01-01T00:00:00Z","EndedAt":"0001-01-01T00:00:00Z","CloseReason":"`
	assertEach(t, []struct {
		Name  string
		Value protocol.SessionRecord
		Want  string
	}{
		{"device_closed", protocol.SessionRecord{CloseReason: protocol.CloseDeviceClosed}, shell + `device_closed","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"read_timeout", protocol.SessionRecord{CloseReason: protocol.CloseReadTimeout}, shell + `read_timeout","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"read_error", protocol.SessionRecord{CloseReason: protocol.CloseReadError}, shell + `read_error","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"protocol_error", protocol.SessionRecord{CloseReason: protocol.CloseProtocol}, shell + `protocol_error","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"rejected", protocol.SessionRecord{CloseReason: protocol.CloseRejected}, shell + `rejected","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"replaced", protocol.SessionRecord{CloseReason: protocol.CloseReplaced}, shell + `replaced","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"context_ended", protocol.SessionRecord{CloseReason: protocol.CloseContextEnded}, shell + `context_ended","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"abandoned_由网关侧补记", protocol.SessionRecord{CloseReason: protocol.CloseAbandoned}, shell + `abandoned","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
		{"未知值原样透传_开放集合", protocol.SessionRecord{CloseReason: protocol.CloseReason("vendor_custom")}, shell + `vendor_custom","BytesIn":0,"BytesOut":0,"FramesIn":0,"FramesOut":0}`},
	})
}

// TestWorkerTaskExecutionStatuses 锁定 worker/repo 两项枚举的全部取值，
// 载体是真实的 workerrepo.ExecutionLog（task_execution_log 行）。
//
// running 与 success／failed 同时出现在别的任务状态词表里，
// 但 TaskTriggeredBy 的 admin_api 是本表独有；terminal 判定
// （TerminalTaskExecutionStatuses = "success failed partial"）不含 running，
// 断言把 running 单列，防止它被并入终态集合。
func TestWorkerTaskExecutionStatuses(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value workerrepo.ExecutionLog
		Want  string
	}{
		{"running_非终态", workerrepo.ExecutionLog{Status: workerrepo.TaskExecutionRunning}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"running","TriggeredBy":"","TriggerReason":""}`},
		{"success", workerrepo.ExecutionLog{Status: workerrepo.TaskExecutionSuccess}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"success","TriggeredBy":"","TriggerReason":""}`},
		{"failed", workerrepo.ExecutionLog{Status: workerrepo.TaskExecutionFailed}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"failed","TriggeredBy":"","TriggerReason":""}`},
		{"partial", workerrepo.ExecutionLog{Status: workerrepo.TaskExecutionPartial}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"partial","TriggeredBy":"","TriggerReason":""}`},
		{"零值_空串", workerrepo.ExecutionLog{}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"","TriggeredBy":"","TriggerReason":""}`},
	})

	assertEach(t, []struct {
		Name  string
		Value workerrepo.ExecutionLog
		Want  string
	}{
		{"cron", workerrepo.ExecutionLog{TriggeredBy: workerrepo.TaskTriggeredByCron}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"","TriggeredBy":"cron","TriggerReason":""}`},
		{"admin_api_不是_admin", workerrepo.ExecutionLog{TriggeredBy: workerrepo.TaskTriggeredByAdminAPI}, `{"ID":0,"TaskCode":"","StartedAt":"0001-01-01T00:00:00Z","CreatedMonth":"0001-01-01T00:00:00Z","Status":"","TriggeredBy":"admin_api","TriggerReason":""}`},
	})
}

// TestChannelRefundStatusVersusPaymentStatus 锁死大小写边界。
//
// payment.ChannelRefundStatus 取自渠道响应，**逐字节大写**（SUCCESS／PROCESSING／
// CLOSED／ABNORMAL），本地不得归一化；money.PaymentStatus 是本地小写词表
// （initiated／paid／closed／refunded／partial_refunded／failed）。
// closed 两边字面量相同（渠道 CLOSED 与本地 closed 大小写不同），
// 正是最容易在归一化时被合并的一对。
//
// 载体分别是真实的 payment.RefundResult 与 payment.OrderPaymentBrief。
func TestChannelRefundStatusVersusPaymentStatus(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value payment.RefundResult
		Want  string
	}{
		{"渠道_SUCCESS_大写", payment.RefundResult{Status: payment.ChannelRefundStatusSuccess}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"SUCCESS","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"渠道_PROCESSING_大写", payment.RefundResult{Status: payment.ChannelRefundStatusProcessing}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"PROCESSING","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"渠道_CLOSED_大写_本地是小写_closed", payment.RefundResult{Status: payment.ChannelRefundStatusClosed}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"CLOSED","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"渠道_ABNORMAL_大写", payment.RefundResult{Status: payment.ChannelRefundStatusAbnormal}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"ABNORMAL","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
		{"未知渠道取值原样透传", payment.RefundResult{Status: payment.ChannelRefundStatus("NEW_CHANNEL_STATE")}, `{"RefundNo":"","MerchantOrderNo":"","TransactionID":"","RefundID":"","Status":"NEW_CHANNEL_STATE","TotalCents":0,"RefundCents":0,"SuccessAt":"0001-01-01T00:00:00Z"}`},
	})

	// 同一个「closed 语义」在两侧的大小写不同。
	assertEach(t, []struct {
		Name  string
		Value any
		Want  string
	}{
		{"渠道_CLOSED", payment.ChannelRefundStatusClosed, `"CLOSED"`},
		{"本地_closed", money.PayStatusClosed, `"closed"`},
		{"本地_没有大写CLOSED", money.PaymentStatus("CLOSED"), `"CLOSED"`},
		{"渠道_没有小写closed", payment.ChannelRefundStatus("closed"), `"closed"`},
	})
}

// TestScanResultDeviceStatusEmptyNotNull 锁死值类型枚举的空值语义：
// money.ScanResult.DeviceStatus 是 devices.DeviceMetaStatus 值类型，
// 无值时序列化为 ""，**不是 null**；改成指针类型会变成 null，
// 消费者将无法区分「未读到状态」与「状态是空串」。
//
// 同一结构体的 port／ports 带 omitempty，两种形态互不影响：
// 零值时两段都省略，非 nil 空切片时输出 []。
func TestScanResultDeviceStatusEmptyNotNull(t *testing.T) {
	assertEach(t, []struct {
		Name  string
		Value money.ScanResult
		Want  string
	}{
		{"零值_device_status为空串", money.ScanResult{}, `{"kind":"","device_id":"","station_id":0,"device_status":""}`},
		{"enabled", money.ScanResult{DeviceStatus: devices.DeviceEnabled}, `{"kind":"","device_id":"","station_id":0,"device_status":"enabled"}`},
		{"disabled", money.ScanResult{DeviceStatus: devices.DeviceDisabled}, `{"kind":"","device_id":"","station_id":0,"device_status":"disabled"}`},
		{"retired", money.ScanResult{DeviceStatus: devices.DeviceRetired}, `{"kind":"","device_id":"","station_id":0,"device_status":"retired"}`},
		{"fault", money.ScanResult{DeviceStatus: devices.DeviceFault}, `{"kind":"","device_id":"","station_id":0,"device_status":"fault"}`},
		{"嵌套端口的device_status同样为空串", money.ScanResult{Port: &money.ScanPort{}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","port":{"port_id":"","device_id":"","port_no":0,"port_status":"","online":false,"available":false,"device_status":""}}`},
		{"嵌套端口有状态", money.ScanResult{Ports: []money.ScanPort{{DeviceStatus: devices.DeviceRetired}}}, `{"kind":"","device_id":"","station_id":0,"device_status":"","ports":[{"port_id":"","device_id":"","port_no":0,"port_status":"","online":false,"available":false,"device_status":"retired"}]}`},
		// ports 带 omitempty：非 nil 空切片同样被省略，不输出 []。
		// 这与 wallet.RefundView.RefundOrders（无 omitempty，空切片输出 []）相反，
		// 两个结构的取舍不同，不能互相推断。
		{"空切片被_omitempty_省略而非输出空数组", money.ScanResult{Ports: []money.ScanPort{}}, `{"kind":"","device_id":"","station_id":0,"device_status":""}`},
		{"nil端口_两段都省略", money.ScanResult{Kind: money.ScanKindDevice}, `{"kind":"device","device_id":"","station_id":0,"device_status":""}`},
	})
}

// TestPointerEnumNullVersusValueEnumEmpty 锁死「指针枚举」与「值枚举」的空值差异。
//
// order.OrderView.StartSource 是 *order.OrderStartSource，nil 序列化为 null；
// devices.DeviceMetaStatus 是值类型，零值序列化为 ""。两者都表示「没有」，
// 但在 JSON 里形态不同，改动任一方的指针性都会改变线上可观察行为。
func TestPointerEnumNullVersusValueEnumEmpty(t *testing.T) {
	const orderNullSource = `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":null,"selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`

	assertEach(t, []struct {
		Name  string
		Value order.OrderView
		Want  string
	}{
		{"指针枚举_nil序列化为null", order.OrderView{CreatedAt: ts}, orderNullSource},
		{"指针枚举_有值为字符串", order.OrderView{CreatedAt: ts, StartSource: ptr(order.StartSourceCard)}, `{"order_id":0,"order_no":"","user_id":"0","device_id":"","port_no":0,"station_id":null,"station_name":null,"status":"","business_status":"","payment_status":"","start_source":"card","selected_scheme_name":null,"selected_package_name":null,"created_at":"2026-01-02T03:04:05Z","started_at":null,"ended_at":null,"duration_seconds":null,"meter_kwh":null,"electric_fee_cents":null,"service_fee_cents":null,"total_fee_cents":null,"refund_status":"","payment_order_id":null,"payment_order_no":null,"payment_order_status":null,"paid_cents":null,"refunded_cents":null,"failure_reason":null,"refund_applicant_id":null,"billing":null,"selected_package":null}`},
	})

	// 同一语义在值类型上就是空串。
	assertJSON(t, money.ScanResult{}, `{"kind":"","device_id":"","station_id":0,"device_status":""}`)
}
