import { Alert, Button } from 'antd';
import type { CSSProperties } from 'react';

/**
 * LoadError 显示数据加载失败的原因和重试按钮。
 * 标题须说明失败的功能，如“订单加载失败”；仅用于读取失败。
 * 提交和保存失败使用表单提示或消息提醒。
 */
export function LoadError({ title, detail, onRetry, style }: {
  /** 点名是哪个功能读不到数据，例如「订单加载失败」。 */
  title: string;
  /** 后端返回的原始原因，直接透出，不另写一套说辞。 */
  detail?: string;
  /** 传入则显示重试按钮；没有可重试的入口时省略。 */
  onRetry?: () => void;
  style?: CSSProperties;
}) {
  return (
    <Alert
      type="error"
      showIcon
      message={title}
      description={detail || undefined}
      style={{ marginBottom: 16, ...style }}
      action={onRetry ? <Button size="small" onClick={onRetry}>重试</Button> : undefined}
    />
  );
}
