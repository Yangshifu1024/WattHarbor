import {formatWalletTxn} from '../../utils/wallet-txn';
import { defineController, getApp, wx } from "../../runtime";
const walletApp=getApp();
export default defineController({
  data:{txns:[],txnsLoading:false,txnsError:'',wallet:null,error:'',loading:false,needsLogin:false},
  onShow(){this._gone=false;this._generation=(this._generation || 0)+1;this.load();},
  onHide(){this._gone=true;this._generation++;},
  onUnload(){this.onHide();},
  onPullDownRefresh(){this.load().finally(()=>wx.stopPullDownRefresh());},
  async login(){try{await walletApp.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
  async load(){
    const generation=++this._generation;
    this._txnsGeneration=(this._txnsGeneration||0)+1;
    this.setData({txns:[],txnsLoading:false,txnsError:'',wallet:null,error:'',loading:false,needsLogin:!walletApp.globalData.token});
    if(!walletApp.globalData.token)return;
    this.setData({loading:true});
    try{const w=await walletApp.request('GET','/user/wallet/balance');if(!this._gone && generation===this._generation)this.setData({wallet:{...w,availableText:(w.available_cents/100).toFixed(2),frozenText:(w.frozen_cents/100).toFixed(2)}});if(!this._gone && generation===this._generation)await this.loadTxns();}
    catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '钱包查询失败',needsLogin:!walletApp.globalData.token});}
    finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
  },
  // 流水使用独立版本，余额刷新、离页和账号变化均使旧响应失效。
  async loadTxns(){
    if(this._gone || !walletApp.globalData.token || !this.data.wallet || this.data.txnsLoading)return;
    const generation=++this._txnsGeneration,view=this._generation,session=walletApp._generation;
    const current=()=>!this._gone && generation===this._txnsGeneration && view===this._generation && session===walletApp._generation;
    this.setData({txnsLoading:true,txnsError:''});
    try{
      const result=await walletApp.request('GET','/user/wallet/txns',{page:1,page_size:3});
      if(!current())return;
      if(!Array.isArray(result.items))throw new Error('资金流水响应异常');
      this.setData({txns:result.items.map(item=>{const row=formatWalletTxn(item);return {...row,amountText:row.amountCurrency};})});
    }catch(e){if(current())this.setData({txnsError:e.message||'流水读取失败'});}
    finally{if(current())this.setData({txnsLoading:false});}
  },
  goCards(){wx.navigateTo({url:'/pages/wallet/cards'});},
  goRecharge(){wx.navigateTo({url:'/pages/wallet/recharge'});},
  goTxns(){wx.navigateTo({url:'/pages/wallet/txns'});},
  goRefund(){wx.navigateTo({url:'/pages/wallet/refund'});},
});
