/** 通过原支付摘要定位充电订单，设备与插座只缩小候选，不能替代支付单号匹配。 */
export async function findPaymentOrder(app, paymentNo, deviceId, portNo, current) {
 const ongoing=await app.request('GET','/user/charge/ongoing');
 if(!current())return null;
 const candidates=[];
 const matches=row=>row && typeof row.order_no==='string' && row.order_no && row.device_id===deviceId && Number(row.port_no)===portNo;
 if(matches(ongoing))candidates.push(ongoing.order_no);
 // 支付回调已创建但很快结束的订单也可恢复；只查最近一页，不猜测历史归属。
 const history=await app.request('GET','/user/charge/history',{page:1,page_size:20});
 if(!current())return null;
 for(const row of history.items || [])if(matches(row) && !candidates.includes(row.order_no))candidates.push(row.order_no);
 for(const no of candidates){
  const detail=await app.request('GET','/user/charge/'+encodeURIComponent(no));
  if(!current())return null;
  if(detail.order_no===no && detail.payment?.order_no===paymentNo)return no;
 }
 return null;
}
