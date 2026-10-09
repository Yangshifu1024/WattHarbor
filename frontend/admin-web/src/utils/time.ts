import dayjs from 'dayjs';

/** formatTime 将后端的 RFC3339 时间转为本地时间；表格时间列统一使用此函数。 */
export function formatTime(value: string | null | undefined): string {
  return value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
}

/** formatMinute 精度到分，用于列表里"最近登录""最近下单"这类只需看分钟的列。 */
export function formatMinute(value: string | null | undefined): string {
  return value ? dayjs(value).format('YYYY-MM-DD HH:mm') : '—';
}
