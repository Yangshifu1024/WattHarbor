import { PageHeader } from "../../runtime/ui";
import { Fragment } from "react";
import { Button, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import controller from "./list.controller";
import "./list.css";

export default function PageView() {
const { data, event } = useController(controller);
const { items, total, loading, error, needsLogin } = data;
return (<><View className="container"><PageHeader title="我的账单" />
{!!(needsLogin) && (<View className="card"><Text>{"登录后查看结算账单"}</Text>
<Button type="primary" onClick={event("login", {})} loading={loading}>{"微信登录"}</Button></View>)}
{(items || []).map((item: any, index: number) => (<View className={`card bill-card${item?.unread ? ' bill-card-unread' : ''}`} data-id={item?.id} onClick={event("open", { id: item?.id })} key={String(item?.id ?? index)}>
<View className="bill-head">
<Text className="bill-no">{item?.bill_no}</Text>
{!!(item?.unread) && (<Text className="bill-unread-tag">{"未读"}</Text>)}
</View>
<View className="bill-amount">{item?.totalText}</View>
<View className="text-secondary">{item?.order_no}{!!(item?.device_id) && (" · " + item?.device_id)}</View>
<View className="text-secondary">{"出账 "}{item?.issuedText}</View>
</View>))}
{!!(loading && !items?.length) && (<View className="card">{"正在读取账单…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(!loading && !error && !items?.length && !needsLogin) && (<View className="card">{"暂无结算账单"}</View>)}
{!!(items?.length > 0 && items?.length < total && !loading) && (<View className="text-secondary bill-more">{"上拉加载更多"}</View>)}
</View>
</>);
}
