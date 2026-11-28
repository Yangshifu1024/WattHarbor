import { useRef, useState } from 'react'
import { Button, Form, Input, Text, View } from '@tarojs/components'
import { getApp, isH5 } from '../../runtime'
import type { ChargeUserProfile } from '../../runtime/models'

/** 标题和号码随当前账号刷新。昵称由用户在微信昵称输入框主动选择，确认后保存。 */
export function ProfileIdentity({ profile, refresh, bind }: { profile: ChargeUserProfile; refresh: () => Promise<void>; bind: () => void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(profile.nickname)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const submitting = useRef(false)

  async function save(value: string) {
    if (submitting.current) return
    const nickname = value.trim()
    if (!nickname) { setNotice('请先填写昵称'); return }
    submitting.current = true
    setBusy(true)
    const app = getApp()
    const generation = app._generation
    try {
      await app.request('POST', '/user/profile/nickname', { nickname })
      if (app._generation === generation) {
        setEditing(false)
        await refresh()
      }
    } catch (error: any) {
      if (app._generation === generation) setNotice(error.message || '昵称保存失败，请重试')
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }

  return <View className='identity-copy'>
    {editing ? <Form onSubmit={e => save(String(e.detail.value.nickname || ''))}>
      <Input type={isH5 ? 'text' : 'nickname'} name='nickname' maxlength={64} aria-label='微信昵称' focus
        value={draft} onInput={e => setDraft(e.detail.value)} onConfirm={isH5 ? e => save(e.detail.value) : undefined} placeholder='填写微信昵称' />
      {notice && <Text className='error'>{notice}</Text>}
      <View className='nickname-actions'><Button disabled={busy} onClick={() => setEditing(false)}>取消</Button>
        <Button formType={isH5 ? undefined : 'submit'} onClick={isH5 ? () => save(draft) : undefined} disabled={busy}><Text>{busy ? '保存中…' : '保存'}</Text></Button></View>
    </Form> : <Button className='identity-name' onClick={() => { setDraft(profile.nickname); setNotice(''); setEditing(true) }}>{profile.nickname || '填写微信昵称'}</Button>}
    {profile.phone_bound && profile.phone ? <Text className='identity-phone'>{profile.phone}</Text>
      : <Button className='identity-phone' onClick={bind}>{profile.phone_bound ? '查看手机号' : '绑定手机号'}</Button>}
  </View>
}
