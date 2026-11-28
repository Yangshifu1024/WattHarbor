import { useRef, useState } from 'react'
import { View, Text, Input, Button } from '@tarojs/components'
import Taro from '@tarojs/taro'
import { application } from './session'

const presets = ['tester-1', 'tester-2']
const accountKey = 'cp_development_account'

/** 仅在开发登录模式挂载。切换沿用原会话注销流程，失败时重新读取真实登录状态。 */
export function DevelopmentAccount({ refresh, disabled }: { refresh: () => Promise<void>; disabled: boolean }) {
  const current: string = Taro.getStorageSync(accountKey) || presets[0]
  const [open, setOpen] = useState(false)
  const [choice, setChoice] = useState(current)
  const [custom, setCustom] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const submitting = useRef(false)
  const accounts = presets.includes(current) ? presets : [...presets, current]
  const account = choice || custom.trim()
  const signedIn = Boolean(application.globalData.token)

  function toggle() {
    setChoice(current)
    setCustom('')
    setNotice('')
    setOpen(!open)
  }

  async function select() {
    if (submitting.current || disabled) return
    if (!/^[a-zA-Z0-9_-]{1,64}$/.test(account)) {
      setNotice('请输入 1 至 64 位字母、数字、下划线或短横线')
      return
    }
    submitting.current = true
    setBusy(true)
    setNotice('')
    try {
      if (application.globalData.refreshToken) await application.logout()
      Taro.setStorageSync(accountKey, account)
      await application.login()
      setOpen(false)
    } catch (error: any) {
      Taro.setStorageSync(accountKey, current)
      setNotice(error.message || '切换失败，请重试')
    } finally {
      await refresh()
      submitting.current = false
      setBusy(false)
    }
  }

  return <>
    <Button className='account-switch' disabled={disabled || busy} onClick={toggle}>{open ? '取消' : '切换账号'}</Button>
    {open && <View className='account-picker'>
      <Text className='picker-title'>选择测试账号</Text>
      <Text className='muted'>钱包与订单按账号独立，支付使用模拟环境。</Text>
      <View className='account-choices'>
        {[...accounts, ''].map(value => <Button key={value} disabled={busy} className={choice === value ? 'picked' : ''}
          onClick={() => { setChoice(value); setNotice('') }}>
          {value || '其他账号'}{signedIn && value === current ? ' · 当前' : ''}
        </Button>)}
      </View>
      {!choice && <Input aria-label='测试账号名称' placeholder='输入测试账号名称' focus maxlength={64} value={custom}
        disabled={busy} onInput={e => setCustom(e.detail.value)} onConfirm={select} />}
      {notice && <Text className='error'>{notice}</Text>}
      <Button type='primary' disabled={busy || !account || (signedIn && account === current)} onClick={select}>
        <Text>{busy ? '正在切换…' : signedIn ? '确认切换' : '登录此账号'}</Text>
      </Button>
    </View>}
  </>
}
