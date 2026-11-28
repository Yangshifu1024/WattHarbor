import { Button, Text, View } from '@tarojs/components'
import { useController } from '../../runtime'
import { ChargingBanner } from '../../runtime/ChargingBanner'
import { TabBar } from '../../runtime/TabBar'
import { MobileHeader, Notice } from '../../runtime/ui'
import controller from './scan-result.controller'

const modes = [{ key: 'amount', label: '按金额' }, { key: 'duration', label: '按时长' }, { key: 'energy', label: '按电量' }]

/** 端口、套餐与价格均来自控制器；支付恢复始终指向原支付意图。 */
export default function PageView() {
  const { data, event } = useController(controller)
  const locked = data.paying || data.startAttempted
  const canChoose = data.selected?.selectable
  const offers = data.offers.filter((offer: any) => offer.mode === data.offerMode)
  const submitLabel = data.canRetryPayment ? '继续原支付单' : data.startAttempted ? '核实原操作'
    : !canChoose ? '请选择端口' : !data.selectedOffer ? '请选择套餐' : '预付 ' + data.selectedOffer.priceText + ' 并充电'
  return <View className='mobile-shell'><View className='mobile-page device-page'>
    <MobileHeader parent={data.stationName || '返回'} />
    <ChargingBanner compact />
    {data.loading && <Notice>正在读取端口与套餐…</Notice>}
    {data.error && <Notice tone='error' retry={event('load')} busy={data.loading || locked}>{data.error}</Notice>}
    {data.deviceId && <>
      <Text className='section-title serial'>{data.number ? data.number + ' 号设备' : '充电设备'}</Text>
      <Text className='muted'>{data.stationName} {data.deviceId}{data.deviceNotice && ' · ' + data.deviceNotice}</Text>
      <View className='section-line spaced'><Text className='section-subtitle'>选择端口</Text><Text className='muted'>{data.singlePort ? '已识别端口' : '空闲 ' + data.ports.filter((p: any) => p.selectable).length + ' / ' + data.ports.length}</Text></View>
      <View className='metal-ports'>{data.ports.map((p: any) => <Button className={'metal-option' + (data.selected?.port_id === p.port_id ? ' chosen' : '')} key={p.port_id} disabled={data.loading || locked || !p.selectable} onClick={event('selectPort', { id: p.port_id })}><Text className='port-number'>{String(p.port_no).padStart(2, '0')}</Text><Text className='muted'>{p.statusLabel}</Text></Button>)}</View>
      {!data.ports.length && <Notice>该设备暂无端口信息</Notice>}
      <View className='section-subtitle spaced'>充电套餐</View>
      {canChoose ? <>
        <View className='metal-modes'>{modes.map(mode => <Button className={data.offerMode === mode.key ? 'mode-current' : ''} key={mode.key} disabled={data.loading || locked} onClick={event('selectMode', { mode: mode.key })}>{mode.label}</Button>)}</View>
        <View className='metal-offers'>{offers.map((offer: any) => <Button className={'metal-option' + (data.selectedOffer?.id === offer.id ? ' chosen' : '')} key={offer.id} disabled={data.loading || locked} onClick={event('selectOffer', { id: offer.id })}>
          <Text className='offer-value'>{offer.priceText}</Text><Text>{offer.name}</Text>
          {offer.mode !== 'amount' && <Text className='muted'>{offer.mode === 'duration' ? offer.duration_minutes + ' 分钟' : offer.energy_wh / 1000 + ' 度'}</Text>}
        </Button>)}</View>
        {!data.loading && !offers.length && <Notice>暂无可用套餐，请切换其他模式</Notice>}
        {data.selectedOffer && <View className='muted spaced-small'>{data.selectedOffer.ruleText}{data.display?.fee_split_inline && <View>{data.selectedOffer.mode === 'duration' ? '支付价格为时长套餐费' : '按冻结电费与服务费单价结算'}</View>}</View>}
        {data.stopWhenFull && <Text className='muted'>支持满充停止，按实际消费退款</Text>}
      </> : <Text className='muted'>{data.selected ? '此端口当前不可充电。' : '请先选择一个空闲端口。'}</Text>}
      <Button type='primary' loading={data.paying} onClick={event('pay')} disabled={data.loading || data.paying || !canChoose || !data.selectedOffer || (data.startAttempted && !!data.paymentNo && !data.canRetryPayment)}>{submitLabel}</Button>
    </>}
    {data.paymentNotice && <Notice>{data.paymentNotice}</Notice>}
    {data.startAttempted && <View>{data.paymentNo && <Text className='muted'>支付单 {data.paymentNo} · {data.feeText}</Text>}<Button disabled={data.paying} onClick={event('charging')}>查看充电状态</Button><Button onClick={event('history')}>查看订单记录</Button></View>}
  </View><TabBar active='home' disabled={data.paying} /></View>
}
