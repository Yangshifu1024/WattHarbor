import { PageHeader } from "../../runtime/ui";
import { Button, Input, Picker, Text, Textarea, View } from "@tarojs/components";
import { isH5, useController } from "../../runtime";
import controller from "./fault.controller";
import "./fault.css";

export default function PageView() {
const { data, event } = useController(controller);
const { deviceId, faultTypeIndex, faultTypes, description, submitting, needsLogin, error, historyError, notice, reports, reportsPage, reportsHasMore, loadingReports, expandedReport, reportHistory, reportHistoryLoading, reportHistoryError } = data;
return (<><View className="container account-page"><PageHeader title="设备报修" />{!!(needsLogin) && (<View className="card"><View>{"登录后提交设备报修"}</View>
<Button className="account-action" type="primary" onClick={event("login", {})}>{"微信登录"}</Button></View>)}
<View className="card"><View className="heading">报修设备</View>
{deviceId ? <View className="fault-device-line"><Text className="device">设备编号 · {deviceId}</Text>
  {data.canChangeDevice && <Button className="account-action" size="mini" disabled={submitting} onClick={event('changeDevice')}>更换设备</Button>}</View> : <View>
  {!isH5 && <Button className="account-action" onClick={event('scanDevice')} disabled={data.resolving}>扫描设备二维码</Button>}
  <Input aria-label="报修设备编号或端口码" placeholder="输入设备编号或端口码" maxlength={2048}
    value={data.manualCode} disabled={data.resolving} onInput={event('inputCode')} onConfirm={event('manualDevice')} />
  <Button className="account-action" onClick={event('manualDevice')} disabled={data.resolving || !data.manualCode.trim()}>{data.resolving ? '正在识别…' : '识别报修设备'}</Button>
  {data.deviceError && <View className="error">{data.deviceError}</View>}
</View>}
<Picker range={faultTypes} value={faultTypeIndex} onChange={event("onType", {})}><View className="field">{"故障类型："}
{faultTypes?.[faultTypeIndex]}
{" ▾"}</View></Picker>
<Textarea className="account-description" placeholder="描述故障现象、发生时间和已尝试的处理方式" maxlength={2000} value={description} onInput={event("onDescription", {})} />
<View className="text-secondary">{"请勿填写个人密码或支付信息。"}</View>
<Button className="account-action" type="primary" onClick={event("submit", {})} loading={submitting} disabled={submitting || !deviceId}>{"提交报修"}</Button></View>
{!!(notice) && (<View className="card success">{notice}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button className="account-action" onClick={event("submit", {})} disabled={submitting}>{"重试"}</Button></View>)}
<View className="card"><View className="heading">{"我的报修"}</View>
{(reports || []).map((item: any, index: number) => (<View className="report-row" key={String(item?.report_id ?? index)}><View className="report-head"><Text>{item?.device_id}</Text>
<Text className="status">{item?.statusText}</Text></View>
<View className="text-secondary">{"报修单 "}
{item?.report_id}
{" · "}
{item?.createdText}</View>
{!!(item?.description) && (<View className="report-description">{item?.description}</View>)}
<Button className="account-action history-toggle" size="mini" data-report-id={item?.report_id} onClick={event("toggleHistory", {"report-id":item?.report_id})}>{expandedReport === item?.report_id ? '收起处理记录' : '查看处理记录'}</Button>
{!!(expandedReport === item?.report_id) && (<View className="history-box">{!!(reportHistoryLoading) && (<View className="text-secondary">{"正在读取处理记录…"}</View>)}
{(reportHistory || []).map((item: any, index: number) => (<View className="history-row" key={String(item?.event_id ?? index)}><View>{item?.eventText}
{" · "}
{item?.createdText}</View>
{!!(item?.fromStatusText || item?.toStatusText) && (<View className="text-secondary">{item?.fromStatusText || '—'}
{" → "}
{item?.toStatusText || '—'}</View>)}
{!!(item?.note) && (<View className="history-note">{item?.note}</View>)}</View>))}
{!!(reportHistoryError) && (<View className="error"><View>{reportHistoryError}</View>
<Button className="account-action" size="mini" data-report-id={item?.report_id} onClick={event("retryReportHistory", {"report-id":item?.report_id})}>{"重试"}</Button></View>)}
{!!(!reportHistoryLoading && !reportHistoryError && reportHistory?.length === 0) && (<View className="text-secondary">{"暂无处理记录"}</View>)}</View>)}</View>))}
{!!(reports?.length === 0 && !loadingReports) && (<View className="text-secondary">{"暂无报修记录"}</View>)}
{!!(historyError) && (<View className="error"><View>{historyError}</View>
<Button className="account-action" onClick={event("retryReports", {})} loading={loadingReports}>{"重试读取"}</Button></View>)}
{!!(reportsHasMore) && (<Button className="account-action" onClick={event("loadMoreReports", {})} loading={loadingReports}>{"加载更多"}</Button>)}
{!!(loadingReports) && (<View className="text-secondary">{"正在读取报修进度…"}</View>)}</View></View></>);
}
