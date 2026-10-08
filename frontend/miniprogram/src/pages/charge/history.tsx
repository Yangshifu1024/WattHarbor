import { PageHeader, Segments } from "../../runtime/ui";
import { Fragment } from "react";
import { Button, Picker, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./history.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { items, total, page, loading, error, needsLogin, statusIndex, statusNames, statusValues } = data;
return (<><View className="container"><PageHeader title="我的订单" />{!!(needsLogin) && (<Button onClick={event("login", {})}>{"登录查看订单"}</Button>)}
{!!(!(needsLogin)) && (<Segments labels={statusNames} selected={statusIndex} onSelect={index => event('onStatus')({ detail: { value: index } })} />)}
{(items || []).map((item: any, index: number) => (<View className="card" data-id={item?.order_no} onClick={event("openDetail", {"id":item?.order_no})} key={String(item?.order_id ?? index)}><View>{item?.order_no}</View>
<View className="tag tag-blue">{item?.statusLabel}</View>
<View>{item?.station_name || item?.device_id}
{" · 端口 "}
{item?.port_no}</View>
<View>{"总费用 "}
{item?.totalText}</View>
<View className="text-secondary">{item?.startedText}</View></View>))}
{!!(error) && (<View className="card"><Text>{error}</Text>
<Button onClick={event("retry", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(!(error) && (!loading && !needsLogin && !items?.length)) && (<View className="card">{"暂无符合条件的订单"}</View>)}
{!!(loading) && (<View className="card">{"正在加载…"}</View>)}
{!!(!(loading) && (items?.length < total)) && (<View className="text-secondary">继续上滑加载更多</View>)}
{!!(!(loading || items?.length < total) && (items?.length)) && (<View className="text-secondary">{"共 "}
{total}
{" 笔，已全部加载"}</View>)}</View>
</>);
}
