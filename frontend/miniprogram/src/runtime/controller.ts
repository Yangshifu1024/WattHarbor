import { useRef, useReducer } from 'react'
import { useLoad, useDidShow, useDidHide, useUnload, usePullDownRefresh, useReachBottom } from '@tarojs/taro'

export function defineController<T>(value: T): T { return value }

// 每次进入页面创建新控制器，保留支付和会话的并发保护；React 渲染界面，Taro 管理页面生命周期。
export function useController(definition: any) {
  const [, redraw] = useReducer(n => n + 1, 0)
  const ref = useRef<any>(null)
  if (!ref.current) {
    const instance = { ...definition, data: JSON.parse(JSON.stringify(definition.data)), _reactMounted: true }
    for (const key of Object.keys(instance)) if (typeof instance[key] === 'function') instance[key] = instance[key].bind(instance)
    instance.setData = (patch: any, callback?: () => void) => {
      for (const [key, value] of Object.entries(patch)) {
        const path = key.replace(/\[(\d+)\]/g, '.$1').split('.')
        let cursor = instance.data
        for (let i = 0; i < path.length - 1; i++) cursor = cursor[path[i]] ||= {}
        cursor[path[path.length - 1]] = value
      }
      if (instance._reactMounted) redraw()
      callback?.()
    }
    ref.current = instance
  }
  const page = ref.current
  useLoad(query => { page._reactMounted = true; page.onLoad?.(query) })
  useDidShow(() => { page._reactMounted = true; page.onShow?.() })
  useDidHide(() => { page.onHide?.() })
  useUnload(() => { page._reactMounted = false; page.onUnload?.() })
  usePullDownRefresh(() => { page.onPullDownRefresh?.() })
  useReachBottom(() => { page.onReachBottom?.() })
  const event = (name: string, dataset: any = {}) => (value: any) => {
    // DOM 的 data-* 可能把数字 ID 转成字符串；向控制器传递渲染时的原始值和类型。
    const currentTarget = { ...value.currentTarget, dataset: { ...value.currentTarget?.dataset, ...dataset } }
    return page[name]?.({ ...value, currentTarget, detail: value.detail || {} })
  }
  return { data: page.data, event, page }
}
