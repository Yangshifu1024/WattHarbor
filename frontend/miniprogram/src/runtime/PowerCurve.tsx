import { useEffect, useRef, useState } from 'react'
import { Canvas, Text, View } from '@tarojs/components'
import Taro from '@tarojs/taro'
import { powerSegments } from '../utils/curve'

/** 双端 Canvas 使用真实功率采样；缺失与超过三分钟的空档断开，不补值。 */
export function PowerCurve({ series, hideUnit = false }: { series: any[]; hideUnit?: boolean }) {
  const canvasId = useRef(`power-${Math.random().toString(36).slice(2)}`).current
  const [drawError, setDrawError] = useState(false)
  const groups = powerSegments(series)
  const points = groups.flat()
  const width = Math.max(160, Math.min(640, Taro.getSystemInfoSync().windowWidth - 64))
  const height = 150
  useEffect(() => {
    if (!points.length) return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout>
    setDrawError(false)
    const draw = (attempt = 0) => {
    if (cancelled) return
    try {
    const ctx = Taro.createCanvasContext(canvasId)
    // H5 的 Canvas 自定义元素异步挂载，页面 effect 可能先于内部 canvas。
    if (process.env.TARO_ENV === 'h5' && !(ctx as any).font) {
      if (attempt < 3) timer = setTimeout(() => draw(attempt + 1), 50)
      else setDrawError(true)
      return
    }
    const left = 34, right = width - 12, top = 16, bottom = height - 24
    const first = Math.min(...points.map((p: any) => p.at))
    const last = Math.max(...points.map((p: any) => p.at))
    const max = Math.max(1, ...points.map((p: any) => p.value)) * 1.15
    const x = (at: number) => left + (last === first ? .5 : (at - first) / (last - first)) * (right - left)
    const y = (value: number) => bottom - value / max * (bottom - top)
    ctx.clearRect(0, 0, width, height)
    ctx.setFontSize(10)
    for (let i = 0; i < 3; i++) {
      const v = max * i / 2
      ctx.setStrokeStyle('#e7eee9'); ctx.setLineWidth(1)
      ctx.beginPath(); ctx.moveTo(left, y(v)); ctx.lineTo(right, y(v)); ctx.stroke()
      ctx.setFillStyle('#718078'); ctx.fillText(String(Math.round(v)), 0, y(v) + 3)
    }
    ctx.setStrokeStyle('#168665'); ctx.setLineWidth(2)
    for (const group of groups) {
      ctx.beginPath()
      group.forEach((p: any, i: number) => i ? ctx.lineTo(x(p.at), y(p.value)) : ctx.moveTo(x(p.at), y(p.value)))
      ctx.stroke()
      for (const p of group) { ctx.beginPath(); ctx.setFillStyle('#168665'); ctx.arc(x(p.at), y(p.value), 2, 0, Math.PI * 2); ctx.fill() }
    }
    ctx.setFillStyle('#718078')
    const time = (at: number) => new Date(at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
    ctx.fillText(time(first), left, height - 6)
    if (last !== first) ctx.fillText(time(last), Math.max(left + 46, right - 34), height - 6)
    ctx.draw()
    } catch { setDrawError(true) }
    }
    Taro.nextTick(() => draw())
    return () => { cancelled = true; clearTimeout(timer) }
  }, [series, width, canvasId])
  return <View>{points.length ? <><Text className='text-secondary'>功率{hideUnit ? '' : '（W）'}</Text>{drawError && <Text className='text-secondary'>曲线暂时无法绘制，请刷新重试</Text>}<Canvas canvasId={canvasId} id={canvasId} style={{ width: `${width}px`, height: `${height}px`, maxWidth: '100%' }} /></> : <View className='text-secondary'>暂无可绘制的功率采样</View>}</View>
}
