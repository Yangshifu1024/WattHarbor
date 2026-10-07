import { Fragment } from "react";
import { Button, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import controller from "./available.controller";
import "./available.css";

export default function PageView() {
const { data, event } = useController(controller);
const { items, loading, error, needsLogin } = data;
return (<><View className="container">
{!!(needsLogin) && (<View className="card"><Text>{"登录后查看可用于下单的优惠券"}</Text>
<Button type="primary" onClick={event("login", {})} loading={loading}>{"微信登录"}</Button></View>)}
{!!(loading && !items?.length) && (<View className="card">{"正在读取可用券…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{(items || []).map((item: any, index: number) => (<View className="coupon card" key={String(item?.grant_id ?? index)}>
<View className="row"><View className="name">{item?.name}</View>
<Text className="tag tag-green">{"可用"}</Text></View>
<View>{item?.typeText}
{" · 满 ¥"}
{item?.minimumText}
{" 可用"}</View>
<View className="text-secondary">{"有效期至 "}{item?.expiresText}</View>
</View>))}
{!!(!loading && !error && !items?.length && !needsLogin) && (<View className="card">{"当前没有可用于下单的优惠券"}</View>)}
<View className="card"><Text className="text-secondary">{"优惠券在支付时自动匹配可用券，最终优惠以服务端结算为准。"}</Text>
<Button onClick={event("goMine", {})}>{"查看我的全部优惠券"}</Button>
<Button onClick={event("goScan", {})}>{"去扫码充电"}</Button></View>
</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
