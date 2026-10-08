import { defineController, getApp, wx } from "../../runtime";
const scanApp=getApp();
import {normalizeCode} from '../../utils/scan';
export default defineController({
 data:{busy:false,error:'',manualCode:''},
 onLoad(query={}){this._reportFault=query.report_fault==='1';this._gone=false;this._unloaded=false;},
 onShow(){this._gone=false;},
 onHide(){this._gone=true;},
 onUnload(){this._gone=true;this._unloaded=true;},
 inputCode(event){this.setData({manualCode:event.detail.value});},
 async openResult(code){
  const valid=normalizeCode(code);
  if(this._gone)return;
  if(this._reportFault){
   const result=await scanApp.request('POST','/user/scan/resolve',{code:valid},false);
   if(this._gone)return;
   if(!result || typeof result.device_id!=='string' || !result.device_id)throw new Error('未识别到设备，请重新扫码');
   await new Promise((resolve,reject)=>wx.navigateTo({url:'/pages/dev/fault?device_id='+encodeURIComponent(result.device_id),success:resolve,fail:reject}));
   return;
  }
  await new Promise((resolve,reject)=>wx.navigateTo({url:'/pages/scan-result/scan-result?code='+encodeURIComponent(valid),success:resolve,fail:reject}));
 },
 async scan(){
  if(this.data.busy)return;
  this.setData({busy:true,error:''});
  try{
   const result=await new Promise((resolve,reject)=>wx.scanCode({onlyFromCamera:true,scanType:['qrCode'],success:resolve,fail:reject}));
   if(!this._gone)await this.openResult(result.result);
  }catch(e){if(!this._gone && !/cancel/i.test(e.errMsg || ''))this.setData({error:e.message || '无法扫码，请检查相机权限或输入设备编号'});}
  finally{if(!this._unloaded)this.setData({busy:false});}
 },
 async manual(){
  if(this.data.busy)return;
  this.setData({busy:true,error:''});
  try{await this.openResult(this.data.manualCode.trim());}
  catch(e){if(!this._gone)this.setData({error:e.message || '设备查询打开失败，请重试'});}
  finally{if(!this._unloaded)this.setData({busy:false});}
 },
});
