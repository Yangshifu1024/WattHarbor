import { useRef, useState } from 'react'
import { Button, Input, View, Text } from '@tarojs/components'
import { AtIcon } from 'taro-ui'
import { isH5, wx } from './platform'
import { normalizeCode } from '../utils/scan'
import './TabBar.css'

const tabs = [
  { key: 'home', label: '首页', icon: 'home', url: '/pages/index/index' },
  { key: 'scan', label: '扫码', icon: 'camera', url: '/pages/scan/scan' },
  { key: 'profile', label: '我的', icon: 'user', url: '/pages/profile/profile' },
]

/** 首页链路含站点与设备。扫码是一次操作，取消原生相机时保留当前页和导航状态。 */
export function TabBar({ active, disabled = false }: { active: 'home' | 'profile'; disabled?: boolean }) {
  const opening = useRef(false)
  const [manual, setManual] = useState(false)
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  function showInput(message = '') {
    setCode('')
    setError(message)
    setManual(true)
  }
  async function submitCode() {
    if (opening.current) return
    opening.current = true
    try {
      const valid = normalizeCode(code.trim())
      await wx.navigateTo({ url: '/pages/scan-result/scan-result?code=' + encodeURIComponent(valid) })
      setManual(false)
    } catch (reason: any) {
      setError(reason?.message || '无法打开设备，请检查编号后重试')
    } finally {
      opening.current = false
    }
  }
  async function select(item: typeof tabs[number]) {
    if (opening.current || disabled) return
    opening.current = true
    try {
      if (item.key !== 'scan') {
        await wx.reLaunch({ url: item.url })
        return
      }
      if (!isH5) {
        let code: string
        try {
          const result = await wx.scanCode({ onlyFromCamera: true, scanType: ['qrCode'] })
          code = normalizeCode(result.result)
        } catch (error: any) {
          if (/cancel/i.test(error?.errMsg || '')) return
          showInput('无法使用相机，请输入设备编号或插座码。')
          return
        }
        await wx.navigateTo({ url: '/pages/scan-result/scan-result?code=' + encodeURIComponent(code) })
        return
      }
      showInput()
    } catch {
      wx.showToast({ title: '页面打开失败，请重试', icon: 'none' })
    } finally {
      opening.current = false
    }
  }
  return <>
    {manual && <View className='scan-overlay'><View className='scan-dialog'>
      <Text className='section-subtitle'>扫码充电</Text>
      <Text className='muted'>粘贴二维码内容，或输入桩身设备编号、插座码。</Text>
      <Input focus maxlength={2048} aria-label='设备编号或插座码' placeholder='设备编号或插座码' value={code}
        onInput={e => setCode(e.detail.value)} onConfirm={submitCode} />
      {error && <Text className='error'>{error}</Text>}
      <Button type='primary' disabled={!code.trim()} onClick={submitCode}>查找设备</Button>
      <Button onClick={() => setManual(false)}>取消</Button>
    </View></View>}
    <View className='tab-bar-spacer' />
    <View className='tab-bar'>{tabs.map(item =>
      <Button key={item.key} disabled={disabled} aria-label={item.label + (active === item.key ? '，当前栏目' : '')}
        className={'tab-item' + (active === item.key ? ' tab-item-active' : '') + (item.key === 'scan' ? ' tab-scan' : '')}
        onClick={() => select(item)}>
        <View className='tab-icon-wrap'>{item.key === 'scan' ? <View className='scan-frame'><View /></View> : <AtIcon value={item.icon} size={19} />}</View>
        <Text className='tab-label'>{item.label}</Text>
      </Button>)}</View>
  </>
}
