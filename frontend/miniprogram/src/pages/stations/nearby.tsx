import { PageHeader } from "../../runtime/ui";
import { Fragment, useState } from "react";
import { Button, Input, Picker, View } from "@tarojs/components";
import { useController, isH5 } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./nearby.controller";

export default function PageView() {
const { data, event } = useController(controller);
const [coordinatesOpen, setCoordinatesOpen] = useState(false);
const { items, markers, latitude, longitude, loading, error, radiusIndex, radii, needsLogin } = data;
return (<><View className="container"><PageHeader title="附近电站" /><Button onClick={event("locate", {})} disabled={loading}>{"获取当前位置"}</Button>
{data.locationNotice && <View className="card text-secondary">{data.locationNotice}</View>}
{isH5 && data.locationNotice && <Button onClick={() => setCoordinatesOpen(!coordinatesOpen)}>{coordinatesOpen ? "收起坐标输入" : "定位不可用？手动输入坐标"}</Button>}
{isH5 && data.locationNotice && coordinatesOpen && <View className="card"><View>浏览器定位不可用时，可按站点坐标搜索</View><Input placeholder="经度，例如 116.397" value={data.longitudeInput || ''} onInput={event('inputLongitude',{})} /><Input placeholder="纬度，例如 39.908" value={data.latitudeInput || ''} onInput={event('inputLatitude',{})} /><Button onClick={event('useCoordinates',{})} disabled={loading}>按坐标查询</Button></View>}
<Picker range={radii} value={radiusIndex} onChange={event("onRadius", {})}><View className="card">{"搜索半径："}
{radii?.[radiusIndex]}
{" 公里 ▾"}</View></Picker>
{!!(latitude != null) && (<StationMap latitude={latitude} longitude={longitude} markers={markers} showLocation={true} onMarkerTap={event("open", {})} style={inlineStyle({"width": "100%", "height": "400rpx"})}></StationMap>)}
{!!(error) && (<View className="card">{error}
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(loading) && (<View className="card">{"正在查询…"}</View>)}
{(items || []).map((item: any, index: number) => (<View className="card" data-id={item?.id} onClick={event("open", {"id":item?.id})} key={String(item?.id ?? index)}><View className="row"><View>{item?.name}
{" · "}
{item?.distanceText}</View><View className="text-secondary">查看设备 ›</View></View>
<View className="text-secondary">{item?.address || '地址未填写'}</View></View>))}
{!!(!loading && !error && !items?.length) && (<View className="card">{"暂无可显示的电站，可重新定位或扩大搜索范围。"}</View>)}</View>
</>);
}
