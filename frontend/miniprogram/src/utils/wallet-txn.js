const labels = {recharge:'充值',pay:'充电消费',refund:'退款',admin_adjust:'人工调整',gift:'赠送'};
const notes={'provider refund succeeded':'原路退款已完成','wechat recharge settled':'充值已到账'};
export function formatWalletTxn(item) {
 const direction=item.direction==='in'?'收入':item.direction==='out'?'支出':'';
 const sign=item.direction==='out'?'-':item.direction==='in'?'+':item.amount_cents<0?'-':'';
 const amount=(Math.abs(item.amount_cents)/100).toFixed(2);
 return {...item,typeText:[labels[item.biz_type]||item.biz_type||item.txn_type||'资金变动',direction].filter(Boolean).join(' · '),amountText:sign+amount,amountCurrency:sign+'¥'+amount,balanceText:(item.balance_after_cents/100).toFixed(2),timeText:new Date(item.created_at).toLocaleString(),remark:notes[item.note]||item.note||''};
}
