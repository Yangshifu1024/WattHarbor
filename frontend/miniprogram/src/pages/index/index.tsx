import { Button, Image, Text, View } from '@tarojs/components'
import { useController } from '../../runtime'
import { TabBar } from '../../runtime/TabBar'
import { ChargingBanner } from '../../runtime/ChargingBanner'
import { MobileHeader, Notice } from '../../runtime/ui'
import cover from '../../assets/station-cover.jpg'
import controller from './index.controller'

/** 站点使用通用封面，空闲端口数取自公开站点摘要。 */
export default function PageView() {
  const { data, event } = useController(controller)
  return <View className='mobile-shell'><View className='mobile-page home-page'>
    <MobileHeader />
    <ChargingBanner />
    <View className='section-line'><Text className='section-title'>充电站</Text><Text className='muted'>{data.moreStations ? '已加载' : '共'} {data.stations.length} 个站点</Text></View>
    {data.stations.map((station: any) => <Button key={station.id} className='list-entry' onClick={event('goStation', { id: station.id })}>
      <View className='station-cover'><Image className={'cover-strip cover-' + station.coverIndex} src={cover} mode='scaleToFill' /></View>
      <View className='entry-copy'><Text className='entry-title'>{station.name || '未命名电站'}</Text><Text className='muted'>{station.address || '地址未填写'}</Text></View>
      <View className='entry-numbers'><Text>{station.available_port_count ?? '—'}</Text><Text className='muted'>空闲端口</Text></View><Text className='chevron'>›</Text>
    </Button>)}
    {data.stationLoading && <View className='station-loading muted'>{data.stations.length ? '正在加载更多站点…' : '正在读取充电站…'}</View>}
    {data.stationError && <Notice tone='error' retry={event('retryStations')}>{data.stationError}</Notice>}
    {!data.stationError && !data.stationLoading && !data.stations.length && <Notice>暂无可显示的充电站</Notice>}
    {data.ongoing && <Button className='list-entry' onClick={event('goOngoing')}><View className='entry-copy'><Text className='entry-title'>{data.ongoing.statusLabel}</Text><Text className='muted'>设备 {data.ongoing.device_id} · 端口 {data.ongoing.port_no ?? '—'}</Text></View><Text className='entry-tail'>查看状态 ›</Text></Button>}
    {data.announcements.length > 0 && <Button className='list-entry' onClick={event('goAnnouncements')}><Text>公告 · {data.announcements[0].title}</Text><Text>›</Text></Button>}
  </View><TabBar active='home' /></View>
}
