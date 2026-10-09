import { defineController, getApp, wx } from "../../runtime";
const chargingApp=getApp();
import {findPaymentOrder} from '../../utils/payment-order';
import {formatOrder} from '../../utils/order';
const display=value=>value==null ? '暂无数据' : String(value);
// 确认费用优先；充电中的预估只读取服务端 live，保留零值与过期提示。
function chargingFee(snapshot){
 if(snapshot.total_cents!=null)return {feeText:'¥'+(snapshot.total_cents/100).toFixed(2),feeLabel:'当前费用',feeNotice:''};
 const live=snapshot.status==='charging' ? snapshot.live : null;
 const unavailable=snapshot.status==='charging' ? snapshot.live_unavailable || live?.fee_unavailable || '' : '';
 if(!unavailable && Number.isFinite(live?.fee?.total_cents))return {
  feeText:'≈¥'+(live.fee.total_cents/100).toFixed(2)+(live.stale ? '（旧）' : ''),feeLabel:'预估费用',
  feeNotice:live.stale ? '计量数据超过 30 秒未更新，显示上次预估；最终以结算账单为准。' : '预估费用仅供参考，最终以结算账单为准。',
 };
 return {feeText:'待结算',feeLabel:'当前费用',feeNotice:unavailable};
}
export default defineController({
 data:{snapshot:null,loading:false,error:'',needsLogin:false,stopping:false,stopNotice:'',curve:null,curveLoading:false,curveError:'',waitingOrder:false},
 onLoad(query){this._orderId=query.order_id || query.order_no;this._paymentNo=query.payment_no;this._deviceId=query.device_id;this._portNo=Number(query.port_no);this._generation=0;this._viewVersion=0;this._gone=false;},
 onShow(){this._gone=false;this.setData({stopping:!!this._stopping});return this.load();},
 onHide(){this.pause();},
 onUnload(){this.pause();},
 pause(){this._gone=true;this._generation++;this._viewVersion++;this._curveGeneration=(this._curveGeneration||0)+1;this.setData({curveLoading:false});clearTimeout(this._timer);this._timer=null;},
 async login(){try{await chargingApp.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
 async load(){
  clearTimeout(this._timer);this._timer=null;
  if(this._gone)return;
  const generation=++this._generation,session=chargingApp._generation;
  if(!this._orderId && !this._paymentNo){this.setData({error:'缺少订单标识',snapshot:null});return;}
  if(!chargingApp.globalData.token){this.setData({needsLogin:true,snapshot:null,loading:false});return;}
  this.setData({loading:true,error:'',needsLogin:false});
  try{
   if(!this._orderId && this._paymentNo){
    const current=()=>!this._gone && generation===this._generation && session===chargingApp._generation;
    const no=await findPaymentOrder(chargingApp,this._paymentNo,this._deviceId,this._portNo,current);
    if(!current())return;
    if(!no){this.setData({waitingOrder:true,snapshot:null});this._timer=setTimeout(()=>this.load(),5000);return;}
    this._orderId=no;this.setData({waitingOrder:false});
   }
   const s=await chargingApp.request('GET','/user/charge/ongoing/snapshot',{order_id:this._orderId});
   if(this._gone || generation!==this._generation || session!==chargingApp._generation)return;
   this.setData({snapshot:{...s,display:s.display||{},statusLabel:formatOrder(s).statusLabel,powerText:display(s.current_power_w ?? s.power_w),voltageText:display(s.voltage_v),temperatureText:display(s.temperature_c),energyText:display(s.charged_kwh),durationText:(s.elapsed_seconds??s.charged_seconds)==null ? '暂无数据' : Math.floor((s.elapsed_seconds??s.charged_seconds)/60)+' 分 '+(s.elapsed_seconds??s.charged_seconds)%60+' 秒',...chargingFee(s)}});
   if(s.status==='charging' && s.display?.show_power)await this.loadCurve();
   if(this._gone || generation!==this._generation || session!==chargingApp._generation)return;
   if(!s.poll_continue)this.setData({stopNotice:''});
   if(s.status!=='charging'){this._curveGeneration=(this._curveGeneration||0)+1;this.setData({curve:null,curveError:'',curveLoading:false});}
   if(s.poll_continue){const delay=Math.min(30000,Math.max(3000,Number(s.next_poll_after_ms)||5000));this._timer=setTimeout(()=>this.load(),delay);}
  }catch(e){if(!this._gone && generation===this._generation && session===chargingApp._generation)this.setData({error:e.message || '充电状态读取失败',snapshot:null,needsLogin:!chargingApp.globalData.token});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 onPullDownRefresh(){return this.load().finally(()=>wx.stopPullDownRefresh());},
 async loadCurve(){
  if(this.data.curveLoading || this.data.snapshot?.status!=='charging')return;
  const generation=this._curveGeneration=(this._curveGeneration||0)+1,view=this._viewVersion,session=chargingApp._generation;
  this.setData({curveLoading:true,curveError:''});
  try{
   const result=await chargingApp.request('GET','/user/charge/ongoing/curve',{order_id:this._orderId,window:'last_30min'});
   if(this._gone || generation!==this._curveGeneration || view!==this._viewVersion || session!==chargingApp._generation)return;
   const series=Array.isArray(result.series)?result.series:[];
   this.setData({curve:{...result,series:series.map(point=>({
    ...point,tsText:point.ts?new Date(point.ts).toLocaleTimeString(): '—',
    powerText:display(point.power_w),currentText:display(point.current_a),voltageText:display(point.voltage_v),temperatureText:display(point.temperature_c),
   }))}});
  }catch(error){if(!this._gone&&generation===this._curveGeneration && view===this._viewVersion && session===chargingApp._generation)this.setData({curveError:error.message||'曲线读取失败，请重试'});}
  finally{if(!this._gone&&generation===this._curveGeneration && view===this._viewVersion && session===chargingApp._generation)this.setData({curveLoading:false});}
 },
 async stopCharge(){
  if(this._stopping || this.data.snapshot?.status!=='charging')return;
  this._stopping=true;
  const viewVersion=this._viewVersion;
  const orderNo=this.data.snapshot.order_no;
  this.setData({stopping:true,stopNotice:''});
  try{
   const confirmed=await new Promise(resolve=>wx.showModal({title:'确认停止充电？',content:'将向设备提交停止请求，请等待设备确认。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
   if(!confirmed || this._gone || viewVersion!==this._viewVersion)return;
   await chargingApp.request('POST','/user/charge/stop',{order_no:orderNo});
   if(!this._gone && viewVersion===this._viewVersion){this.setData({stopNotice:'停止请求已提交，等待设备确认。'});await this.load();}
  }catch(e){if(!this._gone && viewVersion===this._viewVersion)this.setData({stopNotice:e.message || '停止请求失败，请重试'});}
  finally{this._stopping=false;if(!this._gone)this.setData({stopping:false});}
 },
 history(){wx.navigateTo({url:'/pages/charge/history'});},
 invoice(){wx.navigateTo({url:'/pages/invoice/apply'});},
 detail(){if(this._orderId)wx.navigateTo({url:'/pages/charge/detail?order_id='+encodeURIComponent(this._orderId)});},
});
