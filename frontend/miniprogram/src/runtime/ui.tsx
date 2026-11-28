import { ReactNode, useState } from 'react'
import { Button, Text, View } from '@tarojs/components'
import Taro from '@tarojs/taro'
import { AtIcon } from 'taro-ui'
import { isH5, wx } from './platform'

/** 次级页返回原页面；无上级页面时回到首页，不留下失效返回入口。 */
function back() {
  if (Taro.getCurrentPages().length > 1) wx.navigateBack({ delta: 1 })
  else wx.reLaunch({ url: '/pages/index/index' })
}

/** 主流程页头统一品牌和上一级名称。微信详情页沿用原生返回。 */
export function MobileHeader({ parent }: { parent?: string }) {
  if (parent && !isH5) return <View className='native-head-gap' />
  return <View className='brand-bar'>
    {parent ? <Button className='plain-action parent-link' onClick={back}>‹ {parent}</Button> : <>
      <View className='brand-mark'><View /><View /><View /></View><Text>电港充电</Text><Text className='brand-en'>WattHarbor</Text>
    </>}
  </View>
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

/** 三个常用筛选等分显示，更多选项保留控制器原有的索引和查询参数。 */
export function Segments({ labels, selected, onSelect }: {
  labels: string[]
  selected: number
  onSelect: (index: number) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const primaryCount = 3
  function select(index: number) {
    setExpanded(false)
    onSelect(index)
  }
  function option(label: string, index: number, extra = false) {
    const active = selected === index
    return <Button key={label} className={(extra ? 'filter-choice' : 'filter-tab') + (active ? ' current' : '')}
      aria-label={active ? label + '，已选中' : label} onClick={() => select(index)}>{label}</Button>
  }
  return <>
    <View className='filter-tabs'>
      {labels.slice(0, primaryCount).map((label, index) => option(label, index))}
      {labels.length > primaryCount && <Button className={'filter-tab filter-more' + (selected >= primaryCount ? ' current' : '')}
        aria-label={(expanded ? '收起' : '展开') + '更多状态'} onClick={() => setExpanded(!expanded)}>
        {selected >= primaryCount ? labels[selected] : '更多状态'}<Text className='filter-caret'>{expanded ? '▴' : '▾'}</Text>
      </Button>}
    </View>
    {expanded && <View className='filter-options'>
      {labels.slice(primaryCount).map((label, index) => option(label, index + primaryCount, true))}
    </View>}
  </>
}
