import { ReactNode } from 'react'
import { Button, ScrollView, Text, View } from '@tarojs/components'
import Taro from '@tarojs/taro'
import { AtIcon } from 'taro-ui'
import { isH5, wx } from './platform'

/** 次级页返回原页面；无上级页面时回到首页，不留下失效返回入口。 */
function back() {
  if (Taro.getCurrentPages().length > 1) wx.navigateBack({ delta: 1 })
  else wx.reLaunch({ url: '/pages/index/index' })
}

/** 页头展示业务名称；H5 补充返回按钮，微信端复用原生导航。 */
export function PageHeader({ title, subtitle, root = false }: { title: string; subtitle?: string; root?: boolean }) {
  return <View className='page-header'>
    {isH5 && !root && <Button className='back-button' aria-label='返回' onClick={back}><AtIcon value='chevron-left' size={22} /></Button>}
    <View><Text className='page-title'>{title}</Text>{subtitle && <Text className='page-subtitle'>{subtitle}</Text>}</View>
  </View>
}

/** 通用提示保留错误与加载语义，重试由页面控制器处理。 */
export function Notice({ children, retry, busy = false, tone = '' }: { children: ReactNode; retry?: (event: any) => void; busy?: boolean; tone?: string }) {
  return <View className={`card notice ${tone}`}><View>{children}</View>{retry && <Button size='mini' onClick={retry} disabled={busy}>重试</Button>}</View>
}

/** 横向筛选沿用控制器索引，较多状态可横向滚动，不新增查询值。 */
export function Segments({ labels, selected, onSelect }: { labels: string[]; selected: number; onSelect: (index: number) => void }) {
  return <ScrollView scrollX className='segments-scroll'><View className='segments'>{labels.map((label, index) =>
    <Button key={label} className={`segment ${selected === index ? 'is-selected' : ''}`} onClick={() => onSelect(index)}>{label}</Button>
  )}</View></ScrollView>
}
