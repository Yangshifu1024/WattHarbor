import { Button, Text, View } from '@tarojs/components'
import { useController } from '../../runtime'
import { ChargingBanner } from '../../runtime/ChargingBanner'
import { TabBar } from '../../runtime/TabBar'
import { MobileHeader, Notice } from '../../runtime/ui'
import controller from './detail.controller'

/** 设备状态来自站点运行摘要，端口可用性在选择设备后查询。 */
export default function PageView() {
  const { data, event } = useController(controller)
  return <View className='mobile-shell'><View className='mobile-page station-page'>
    <MobileHeader parent='首页' />
    <ChargingBanner compact />
    {data.loading && <Notice>正在读取电站…</Notice>}
    {data.error && <Notice tone='error' retry={event('load')} busy={data.loading}>{data.error}</Notice>}
    {data.station && <>
      <View className='section-title'>{data.station.name || '未命名电站'}</View>
      <Text className='muted'>{data.station.address || '地址未填写'}</Text>
      <View className='section-line spaced'><Text className='section-subtitle'>设备列表</Text><Text className='muted'>{data.devices.length} 台设备</Text></View>
      {data.devices.map((d: any) => <Button className='list-entry' key={d.device_id} disabled={!d.clickable} onClick={event('openDevice', { id: d.device_id })}>
        <View className='device-symbol'><View /></View><View className='entry-copy'><Text className='entry-title'>{d.number} 号设备</Text><Text className='muted serial'>{d.device_id}</Text></View><View className='entry-numbers'><Text>{d.available ?? '—'} / {d.total ?? '—'}</Text><Text className='muted'>{d.clickable ? '空闲 / 总端口' : d.online ? '暂停服务' : d.statusText}</Text></View><Text className='chevron'>›</Text>
      </Button>)}
      {!data.devices.length && <Notice>该站点暂无设备记录</Notice>}
      {data.announcements.map((item: any, i: number) => <View className='card' key={item.id ?? i}><Text className='entry-title'>{item.title || '站内公告'}</Text><Text className='muted'>{item.content}</Text></View>)}
    </>}
  </View><TabBar active='home' /></View>
}
