// 账单展示格式化：金额为整数分，时间缺失时不以零值伪装为已出账。
function money(cents) { return cents == null ? '待结算' : `¥${(cents / 100).toFixed(2)}`; }
function timeText(value, empty) { if(!value)return empty; const date=new Date(value); return Number.isFinite(date.getTime())?date.toLocaleString('zh-CN',{hour12:false}):'时间无效'; }
// 当前接口使用 read 布尔凭据；兼容已有 read_at，时间只用于展示。
function formatBill(bill) {
  return { ...bill, totalText: money(bill.total_cents), electricText: money(bill.electric_cents), serviceText: money(bill.service_cents),
    issuedText: timeText(bill.issued_at, '出账时间未知'), readText: timeText(bill.read_at, ''), unread: !(bill.read === true || bill.read_at) };
}
export { formatBill };
