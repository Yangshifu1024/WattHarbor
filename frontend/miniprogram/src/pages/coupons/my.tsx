import { PageHeader, Segments } from "../../runtime/ui";
import { Fragment } from "react";
import { Button, Picker, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./my.controller";
import "./my.css";

export default function PageView() {
const { data, event } = useController(controller);
const { items, total, page, pageSize, statusIndex, statusNames, loading, error, needsLogin } = data;
return (<><View className="container"><PageHeader title="我的优惠券" /><View className="page-footnote">充电下单暂不支持使用优惠券</View>{!!(needsLogin) && (<View className="card"><View>{"登录后查看优惠券"}</View>
<Button type="primary" onClick={event("login", {})}>{"微信登录"}</Button></View>)}
{!!(!(needsLogin)) && (<Segments labels={statusNames} selected={statusIndex} onSelect={index => event('onStatus')({ detail: { value: index } })} />)}
{(items || []).map((item: any, index: number) => (<View className="coupon card" key={String(item?.grant_id ?? index)}><View className="row"><View className="name">{item?.name}</View>
<View className="discount">{item?.discountText}</View></View>
<View>{item?.typeText}
{" · 满 ¥"}
{item?.minimumText}
{" 可用"}</View>
<View className="text-secondary">{"有效期至 "}
{item?.expiresText}</View>
<View className="tag">{item?.statusText}</View></View>))}
{!!(loading) && (<View className="card">{"正在读取优惠券…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(!loading && !error && !items?.length && !needsLogin) && (<View className="card">{"当前没有"}
{statusNames?.[statusIndex]}
{"的优惠券"}</View>)}
{!!(items?.length < total && !loading) && (<View className="text-secondary">继续上滑加载更多</View>)}
{!!(!needsLogin) && (<Button onClick={event("goAvailable", {})}>{"查询可用券"}</Button>)}
{!!(!needsLogin) && (<Button onClick={event("goScan", {})}>{"去扫码充电"}</Button>)}</View>
</>);
}
