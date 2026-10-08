import { Button, Image, Text, View } from '@tarojs/components'
import { AtIcon } from 'taro-ui'
import { useController } from '../../runtime'
import { TabBar } from '../../runtime/TabBar'
import { Notice, PageHeader } from '../../runtime/ui'
import { DevelopmentAccount } from '../../runtime/development'
import { developmentLogin } from '../../runtime/platform'
import controller from './profile.controller'

const QUICK = [{ title: '订单', icon: 'file-generic', action: 'goOrders' }, { title: '账单', icon: 'list', action: 'goBills' }, { title: '退款', icon: 'reload', action: 'goRefunds' }, { title: '优惠券', icon: 'tags', action: 'goCoupons' }]
const SERVICES = [{ title: '我的发票', icon: 'file-generic', action: 'goInvoices' }, { title: '在线卡', icon: 'credit-card', action: 'goCards' }, { title: '绑定手机号', icon: 'phone', action: 'goPhoneBind' }, { title: '设备报修', icon: 'settings', action: 'goFault' }, { title: '公告', icon: 'notification', action: 'goAnnouncements' }]
/** 我的聚合账户入口；未登录不显示虚构余额，私有数据沿用控制器会话隔离。 */
export default function PageView() {
  const { data, event } = useController(controller)
  const profile = data.profile
  return <><View className='container'>
    <PageHeader title='我的' root />
    <DevelopmentAccount />
    {data.needsLogin && <View className='card'><View className='heading'>登录后管理你的充电记录</View><View className='text-secondary'>查看余额、订单、账单与退款进度</View><Button type='primary' loading={data.loggingIn} disabled={data.loggingIn} onClick={event('login')}>{developmentLogin ? '开发账号登录' : '微信登录'}</Button></View>}
    {data.loading && <Notice>正在读取个人资料…</Notice>}
    {data.error && <Notice tone='error' retry={data.needsLogin ? undefined : event('refresh')} busy={data.loading}>{data.error}</Notice>}
    {profile && <>
      <View className='profile-head'>{profile.avatar_url ? <Image className='profile-avatar' src={profile.avatar_url} /> : <View className='profile-avatar'><AtIcon value='user' size={30} color='#718078' /></View>}<View><View className='profile-name'>{profile.nickname || '充电用户'}</View><View className='text-secondary' onClick={event('goPhoneBind')}>手机号{profile.phone_bound ? '已绑定' : '未绑定'} ›</View></View></View>
      <View className='card' onClick={event('goWallet')}><View className='row'><Text className='text-secondary'>可用余额</Text><Text className='text-secondary'>我的钱包 ›</Text></View><View className='money-large'>¥{profile.balanceText}</View><View className='text-secondary'>冻结金额 ¥{profile.frozenText}</View>{profile.wallet?.status === 'frozen' && <View className='error'>钱包已冻结，请联系客服</View>}</View>
      <View className='card service-grid four'>{QUICK.map(item => <Button className='service-button' key={item.title} onClick={event(item.action)}><AtIcon value={item.icon} size={24} /><Text>{item.title}</Text></Button>)}</View>
    </>}
    <View className='card'>{SERVICES.map(item => <View className='menu-row' key={item.title} onClick={event(item.action)}><View className='menu-label'><AtIcon value={item.icon} size={20} /><Text>{item.title}</Text></View><Text className='text-secondary'>›</Text></View>)}</View>
    {!data.needsLogin && <Button onClick={event('onLogout')}>退出登录</Button>}
  </View><TabBar active='profile' /></>
}
