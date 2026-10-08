import { PageHeader } from "../../runtime/ui";
import { Fragment } from "react";
import { Button, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import controller from "./detail.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { bill, loading, error, needsLogin, readSaving, readNotice, readError } = data;
return (<><View className="container"><PageHeader title="账单详情" />
{!!(needsLogin) && (<View className="card"><Text>{"登录后查看结算账单"}</Text>
<Button type="primary" onClick={event("login", {})} loading={loading}>{"微信登录"}</Button></View>)}
{!!(loading && !bill) && (<View className="card">{"正在读取账单…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(bill) && (<Fragment>
<View className="card">
<Text className="text-secondary">{"账单号 "}{bill?.bill_no}</Text>
<View className="bill-amount">{bill?.totalText}</View>
<View>{"电费 "}{bill?.electricText}{" · 服务费 "}{bill?.serviceText}</View>
</View>
<View className="card">
<View>{"来源订单 "}{bill?.order_no || '—'}</View>
<View>{"设备 "}{bill?.device_id || '—'}</View>
<View>{"出账时间 "}{bill?.issuedText}</View>
<View>{"已读状态 "}{bill?.unread ? '未读' : (bill?.readText || '已读')}</View>
{!!(readSaving) && (<View className="text-secondary">{"正在保存已读状态…"}</View>)}
{!!(readNotice) && (<View className="success">{readNotice}</View>)}
{!!(readError) && (<View className="error">{readError}</View>)}
</View>
<Button onClick={event("orderDetail", {})} disabled={!bill?.order_no}>{"查看来源订单"}</Button>
</Fragment>)}
</View>
</>);
}
