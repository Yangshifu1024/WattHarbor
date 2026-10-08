import { Button, Text, View } from '@tarojs/components'
import { useController } from '../../runtime'
import { PageHeader, Notice } from '../../runtime/ui'
import controller from './detail.controller'

/** 站点详情先列设备，不在站点层混排插座与套餐。 */
export default function PageView() {
  const { data, event } = useController(controller)
  return <View className='container'>
    <PageHeader title='电站详情' />
    {data.loading && <Notice>正在读取电站…</Notice>}
    {data.error && <Notice tone='error' retry={event('load')} busy={data.loading}>{data.error}</Notice>}
    {data.station && <>
      <View className='card'><View className='page-title'>{data.station.name || '未命名电站'}</View><Text className='text-secondary'>{data.station.address || '地址未填写'}</Text></View>
      {(data.announcements || []).map((notice: any, i: number) => <View className='card' key={notice.id ?? i}><View className='heading'>{notice.title || '站内公告'}</View><Text className='text-secondary'>{notice.content}</Text></View>)}
      <View className='section-heading'>设备列表</View>
      {(data.devices || []).map((device: any) => <View className='card' key={device.device_id}><View className='row'><Text className='heading'>设备 {device.device_id}</Text><Text className={`tag ${device.online ? 'tag-green' : 'tag-grey'}`}>{device.statusText}</Text></View><View className='text-secondary'>最近上报：{device.lastHeartbeatText}</View><Button type={device.clickable ? 'primary' : 'default'} disabled={!device.clickable} onClick={event('openDevice', { id: device.device_id })}>{device.clickable ? '选择此设备' : device.statusText === '离线' ? '设备离线' : '暂不可选择'}</Button></View>)}
      {!data.devices?.length && !data.loading && !data.error && <Notice>该站点暂无设备记录</Notice>}
      <View className='page-footnote'>先选择设备，再选择插座与套餐</View>
    </>}
  </View>
}
