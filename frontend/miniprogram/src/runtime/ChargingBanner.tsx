import { Image, Text, View } from '@tarojs/components'
import banner from '../assets/charging-banner.jpg'

/** 三个公开浏览页共用本地品牌广告，详情页压低高度以留出设备与端口空间。 */
export function ChargingBanner({ compact = false }: { compact?: boolean }) {
  return <View className={compact ? 'charging-banner compact' : 'charging-banner'}>
    <Image className='banner-image' src={banner} mode='aspectFill' />
    <Text className='ad-label'>广告</Text>
    <View className='banner-copy'><Text className='banner-title'>为城市骑行充电</Text><Text>电港两轮车充电服务</Text></View>
  </View>
}
