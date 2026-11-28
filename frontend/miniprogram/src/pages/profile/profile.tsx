import { Button, Image, Text, View } from '@tarojs/components'
import { AtIcon } from 'taro-ui'
import { useController } from '../../runtime'
import { TabBar } from '../../runtime/TabBar'
import { MobileHeader, Notice } from '../../runtime/ui'
import { DevelopmentAccount } from '../../runtime/development'
import { developmentLogin } from '../../runtime/platform'
import { formatOrder } from '../../utils/order'
import controller from './profile.controller'
import { ProfileIdentity } from './ProfileIdentity'

const services = [
  ['在线卡', 'credit-card', 'goCards'], ['我的账单', 'list', 'goBills'], ['申请余额退款', 'reload', 'goRefunds'],
  ['我的发票', 'file-generic', 'goInvoices'], ['优惠券', 'tags', 'goCoupons'], ['绑定手机号', 'phone', 'goPhoneBind'],
  ['设备报修', 'settings', 'goFault'], ['公告', 'volume-plus', 'goAnnouncements'],
]

/** 钱包与订单均使用当前会话的数据；匿名状态仅提供登录和公开入口。 */
export default function PageView() {
  const { data, event, page } = useController(controller)
  const p = data.profile
  const order = data.recentOrder && formatOrder(data.recentOrder)
  return <View className='mobile-shell'><View className='mobile-page'>
    <MobileHeader />
    <View className='account-head'>
      {p?.avatar_url ? <Image className='avatar' src={p.avatar_url} /> : <View className='avatar'><AtIcon value='user' size={23} /></View>}
      {p ? <ProfileIdentity key={p.user_id} profile={p} refresh={page.refresh} bind={page.goPhoneBind} />
        : <Text className='section-title identity-copy'>{data.needsLogin ? '未登录' : '我的账户'}</Text>}
      {developmentLogin && <DevelopmentAccount refresh={page.refresh} disabled={data.loggingIn || data.loading} />}
    </View>
    {data.needsLogin && <Button type='primary' loading={data.loggingIn} disabled={data.loggingIn} onClick={event('login')}>{developmentLogin ? '开发账号登录' : '微信登录'}</Button>}
    {data.loading && <Notice>正在读取个人资料…</Notice>}
    {data.error && <Notice tone='error' retry={event(data.needsLogin ? 'login' : 'refresh')}>{data.error}</Notice>}
    {p && <>
      <Button className='wallet-panel' onClick={event('goWallet')}><View className='section-line'><Text>我的钱包</Text><Text>查看钱包 ›</Text></View><Text>可用余额</Text><Text className='wallet-amount'>¥ {p.balanceText}</Text>{Number(p.frozenText) > 0 && <Text>冻结金额 ¥{p.frozenText}</Text>}{p.wallet?.status === 'frozen' && <Text>钱包已冻结，请联系客服</Text>}</Button>
      <View className='section-line'><Text className='section-subtitle'>充电订单</Text><Button className='plain-action' onClick={event('goOrders')}>全部订单 ›</Button></View>
      {order ? <Button className='list-entry' onClick={event('openRecentOrder')}><View className='entry-copy'><Text className='entry-title'>{order.station_name || order.device_id} · {order.port_no ?? '—'} 号端口</Text><Text className='muted'>{order.statusLabel} · {order.totalText}</Text></View><Text>›</Text></Button> : <Text className='muted'>{data.orderLoading ? '正在读取订单…' : data.orderError ? '最近订单读取失败，可查看全部订单。' : '暂无充电订单'}</Text>}
    </>}
    <View className='section-subtitle spaced'>账户服务</View>
    {services.map(([title, icon, action]) => <Button className='list-entry service-entry' key={action} onClick={event(action)}><AtIcon value={icon} size={20} /><Text className='entry-copy'>{title}</Text><Text>›</Text></Button>)}
    {p && <Button className='account-logout' onClick={event('onLogout')}>退出登录</Button>}
  </View><TabBar active='profile' /></View>
}
