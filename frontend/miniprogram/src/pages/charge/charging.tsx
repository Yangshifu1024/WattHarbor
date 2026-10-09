import { Button, Text, View } from '@tarojs/components'
import { AtIcon } from 'taro-ui'
import { useController } from '../../runtime'
import { PageHeader, Notice } from '../../runtime/ui'
import { PowerCurve } from '../../runtime/PowerCurve'
import controller from './charging.controller'

/** 按服务端返回的订单状态和 display 配置展示充电数据。 */
export default function PageView() {
  const { data, event } = useController(controller)
  const s = data.snapshot
  return <View className='container'>
    <PageHeader title='充电状态' />
    {data.needsLogin && <Button type='primary' onClick={event('login')}>登录查看充电状态</Button>}
    {data.loading && !s && <Notice>正在读取订单状态…</Notice>}
    {data.waitingOrder && !data.needsLogin && <Notice>支付与订单结果确认中，请勿重复付款。<Button onClick={event('history')}>查看订单记录</Button></Notice>}
    {data.error && <Notice tone='error' retry={event('load')} busy={data.loading}>{data.error}</Notice>}
    {s && <>
      <View className='charging-hero'><Text className={`tag ${s.status === 'charging' ? 'tag-green' : 'tag-orange'}`}>{s.statusLabel}</Text><View className='charging-ring'><AtIcon value='lightning-bolt' size={48} /></View><View className='money-large' style={{ fontSize: '30px' }}>{s.durationText}</View><View className='text-secondary'>已充电时长</View><View className='text-secondary'>设备 {s.device_id || '—'} · {s.port_no ?? '—'} 号插座</View></View>
      {s.status === 'paid' && <Notice>支付已确认，设备启动确认中；系统核实同一个启动操作，请勿重复下单。</Notice>}
      {s.status === 'pending_payment' && <Notice>订单等待支付确认，设备尚未开始充电。</Notice>}
      {!s.poll_continue && <Notice>订单已结束，状态刷新已停止。</Notice>}
      <View className='card metric-grid'>
        {s.display?.show_power && <View><Text className='text-secondary'>实时功率</Text><Text className='metric-value'>{s.powerText}{s.powerText !== '暂无数据' && !s.display.hide_unit ? ' W' : ''}</Text></View>}
        {s.display?.show_energy && <View><Text className='text-secondary'>累计电量</Text><Text className='metric-value'>{s.energyText}{s.energyText !== '暂无数据' && !s.display.hide_unit ? ' kWh' : ''}</Text></View>}
        <View><Text className='text-secondary'>{s.feeLabel}</Text><Text className='metric-value'>{s.feeText}</Text></View>
      </View>
      {s.feeNotice && <Notice>{s.feeNotice}</Notice>}
      {s.status === 'charging' && <View className='card'>
        {!s.telemetry_available && <View className='text-secondary'>暂未收到实时遥测数据。</View>}
        <View className='money-row'><Text>电压</Text><Text>{s.voltageText}{s.voltageText !== '暂无数据' ? ' V' : ''}</Text></View>
        <View className='money-row'><Text>温度</Text><Text>{s.temperatureText}{s.temperatureText !== '暂无数据' ? ' °C' : ''}</Text></View>
        <View className='row'><Text className='heading'>近 30 分钟遥测</Text><Button size='mini' disabled={data.curveLoading} loading={data.curveLoading} onClick={event('loadCurve')}>刷新</Button></View>
        {data.curveError && <Notice tone='error' retry={event('loadCurve')} busy={data.curveLoading}>{data.curveError}</Notice>}
        {data.curve && <>
          {s.display?.show_power && <PowerCurve series={data.curve.series || []} hideUnit={s.display.hide_unit} />}
          {!data.curve.series?.length && <View className='text-secondary'>该时间段没有设备遥测。</View>}
          <View className='text-secondary'>采样间隔 {data.curve.sample_interval_seconds ?? '—'} 秒</View>
          {!s.display?.show_power && (data.curve.series || []).map((point: any, i: number) => <View className='curve-row' key={point.ts ?? i}>{point.tsText} · {point.currentText} A · {point.voltageText} V · {point.temperatureText} °C</View>)}
        </>}
      </View>}
      {s.billing_status === 'manual_review' && <Notice>计量不足，待管理员核对。暂不猜算收费。</Notice>}
      {(s.card_operations || []).map((item: any) => <View className='card' key={item.operation_id}>{item.kind === 'extend' ? '加时' : '刷卡启动'} {item.minutes} 分钟 · {item.status === 'confirming' ? '确认中' : item.status === 'confirmed' ? '已确认' : '失败，已补偿退款'}</View>)}
      {(s.tariff_lines || []).map((line: string, i: number) => <View className='card' key={i}>{line}</View>)}
      {s.rule_description && <View className='card'>{s.rule_description}</View>}
      <Button onClick={event('detail')}>查看订单详情</Button>
      {s.status === 'charging' && <Button className='danger-button' disabled={data.stopping || data.loading} loading={data.stopping} onClick={event('stopCharge')}>停止充电</Button>}
      {data.stopNotice && <Notice>{data.stopNotice}</Notice>}
      {!s.poll_continue && ['completed', 'refunding', 'refunded'].includes(s.status) && <Button onClick={event('invoice')}>申请发票</Button>}
    </>}
  </View>
}
