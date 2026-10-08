import { Button, Text, View } from '@tarojs/components'
import { useController } from '../../runtime'
import { PageHeader, Notice } from '../../runtime/ui'
import controller from './scan-result.controller'

const MODES = [{ key: 'amount', label: '按金额' }, { key: 'duration', label: '按时长' }, { key: 'energy', label: '按电量' }]
/** 插座与套餐只消费控制器校验后的数据，提交字段与支付恢复仍由控制器负责。 */
export default function PageView() {
  const { data, event } = useController(controller)
  const locked = data.paying || data.startAttempted
  const offers = (data.offers || []).filter((offer: any) => offer.mode === data.offerMode)
  return <View className='container'>
    <PageHeader title='选择充电' subtitle={data.deviceId ? `设备 ${data.deviceId}` : '请先核对设备与插座编号'} />
    {data.loading && <Notice>正在读取插座与套餐…</Notice>}
    {data.error && <Notice tone='error' retry={event('load')} busy={data.loading || locked}>{data.error}</Notice>}
    {data.deviceId && <>
      <View className='card'><View className='row'><Text>设备 {data.deviceId}</Text><Text className='tag'>{data.deviceStatusLabel}</Text></View><View className='text-secondary'>{data.deviceNotice || '请核对插座编号，再选择充电套餐'}</View></View>
      <View className='step-title'><Text className='step-number'>1</Text>选择插座</View>
      <View className='port-grid'>{(data.ports || []).map((port: any) => <Button className={`port-button ${data.selected?.port_id === port.port_id ? 'is-selected' : ''}`} key={port.port_id} disabled={data.loading || locked || !port.selectable} onClick={event('selectPort', { id: port.port_id })}><Text>{String(port.port_no).padStart(2, '0')}</Text><Text className='text-secondary'>{port.statusLabel}</Text></Button>)}</View>
      {!data.ports?.length && <Notice>该设备暂无插座信息</Notice>}
    </>}
    {data.selected?.selectable && <>
      <View className='step-title'><Text className='step-number'>2</Text>选择套餐</View>
      <View className='segments' style={{ width: '100%', marginBottom: '16px' }}>{MODES.map(mode => <Button className={`segment ${data.offerMode === mode.key ? 'is-selected' : ''}`} style={{ flex: 1, padding: 0 }} disabled={data.loading || locked} key={mode.key} onClick={event('selectMode', { mode: mode.key })}>{mode.label}</Button>)}</View>
      <View className='offer-grid'>{offers.map((offer: any) => <Button className={`offer-button ${data.selectedOffer?.id === offer.id ? 'is-selected' : ''}`} key={offer.id} disabled={locked || data.loading} onClick={event('selectOffer', { id: offer.id })}><Text className='offer-price'>{offer.priceText}{data.selectedOffer?.id === offer.id ? ' ✓' : ''}</Text><Text className='offer-name'>{offer.name}</Text><Text className='text-secondary'>{offer.mode === 'duration' ? `${offer.duration_minutes} 分钟` : offer.mode === 'energy' ? `${offer.energy_wh / 1000} 度` : '预付金额'}</Text></Button>)}</View>
      {!data.loading && !offers.length && <Notice>暂无可用套餐，请切换其他模式</Notice>}
      {data.selectedOffer && <View className='offer-rule'>{data.selectedOffer.ruleText}{data.display?.fee_split_inline && <View>{data.selectedOffer.mode === 'duration' ? '支付价格为时长套餐费' : '按冻结电费与服务费单价结算'}</View>}</View>}
      {data.stopWhenFull && <View className='page-footnote'>支持满充停止，按实际消费退款</View>}
    </>}
    {data.selected && !data.selected.selectable && <Notice>此插座当前不可充电，请选择其他空闲插座</Notice>}
    {data.paymentNotice && <Notice>{data.paymentNotice}</Notice>}
    {data.startAttempted && <View className='card'>{data.paymentNo && <View className='text-secondary'>支付单 {data.paymentNo} · {data.feeText}</View>}<Button onClick={event('charging')} disabled={data.paying}>查看充电状态</Button><Button onClick={event('history')}>查看订单记录</Button></View>}
    {!data.startAttempted && <Button onClick={event('rescan')} disabled={data.paying}>重新扫码</Button>}
    <View className='checkout-spacer' />
    <View className='fixed-checkout'><View><Text className='text-secondary'>{data.selected ? `已选 ${data.selected.port_no} 号插座` : '请选择插座'}</Text><View className='checkout-amount'>{data.selectedOffer?.priceText || '请选择套餐'}</View></View><Button type='primary' onClick={event('pay')} loading={data.paying} disabled={data.loading || data.paying || !data.selected?.selectable || !data.selectedOffer || (data.startAttempted && !!data.paymentNo && !data.canRetryPayment)}>{data.canRetryPayment ? '继续原支付单' : data.startAttempted ? '核实原操作' : '支付并充电'}</Button></View>
  </View>
}
