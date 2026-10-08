import { Button, Text, View } from '@tarojs/components'
import { AtIcon } from 'taro-ui'
import { useController } from '../../runtime'
import { PageHeader, Notice } from '../../runtime/ui'
import controller from './wallet.controller'

const ENTRIES = [{ title: '余额流水', icon: 'list', action: 'goTxns' }, { title: '钱包退款', icon: 'reload', action: 'goRefund' }, { title: '在线卡', icon: 'credit-card', action: 'goCards' }]
/** 钱包区分余额读取与流水读取，流水失败不撤销余额。 */
export default function PageView() {
  const { data, event } = useController(controller)
  return <View className='container'>
    <PageHeader title='我的钱包' />
    {data.needsLogin && <View className='card'><View>登录后查看钱包</View><Button type='primary' onClick={event('login')}>微信登录</Button></View>}
    {data.loading && <Notice>正在读取钱包…</Notice>}
    {data.error && <Notice tone='error' retry={event('load')}>{data.error}</Notice>}
    {data.wallet && <>
      <View className='card'><View className='text-secondary'>可用余额</View><View className='money-large'>¥{data.wallet.availableText}</View><View className='text-secondary'>冻结金额 ¥{data.wallet.frozenText}</View>{data.wallet.status === 'frozen' && <Notice tone='error'>钱包已冻结，请联系客服</Notice>}<Button type='primary' onClick={event('goRecharge')}>充值</Button></View>
      <View className='card service-grid'>{ENTRIES.map(item => <Button className='service-button' key={item.title} onClick={event(item.action)}><AtIcon value={item.icon} size={24} /><Text>{item.title}</Text></Button>)}</View>
      <View className='section-heading'>最近流水</View>
      {data.txnsLoading && <Notice>正在读取流水…</Notice>}
      {data.txnsError && <Notice tone='error' retry={event('loadTxns')}>{data.txnsError}</Notice>}
      {(data.txns || []).map((item: any) => <View className='card' key={item.txn_no}><View className='row'><Text>{item.typeText}</Text><Text className={item.amount_cents > 0 ? 'success' : ''}>{item.amountText}</Text></View><View className='text-secondary'>{item.timeText}</View></View>)}
      {!data.txnsLoading && !data.txnsError && !data.txns?.length && <Notice>暂无资金流水</Notice>}
      <View className='page-footnote'>充电订单退款请在订单详情查看</View>
    </>}
  </View>
}
