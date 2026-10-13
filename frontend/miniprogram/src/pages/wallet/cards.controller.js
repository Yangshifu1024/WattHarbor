import { defineController, getApp, wx } from "../../runtime";
import { beginOperation, operationVisible, finishOperation, syncOperations } from '../../utils/page-operation';
const app=getApp();
export default defineController({
 data:{cards:[],loading:false,error:'',needsLogin:false,busy:false},
 onShow(){this._gone=false;syncOperations(this,app,['busy']);this._generation=(this._generation||0)+1;return this.load();},
 onHide(){this._gone=true;this._generation++;},onUnload(){this._unloaded=true;this.onHide();},
 async login(){try{await app.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message});}},
 async load(){const current=++this._generation;this.setData({cards:[],error:'',needsLogin:!app.globalData.token});if(!app.globalData.token)return;this.setData({loading:true});try{const d=await app.request('GET','/user/cards');if(!this._gone&&current===this._generation)this.setData({cards:d.items.map(c=>({...c,label:{active:'正常',lost:'挂失',disabled:'禁用'}[c.status]||c.status}))});}catch(e){if(!this._gone&&current===this._generation)this.setData({error:e.message});}finally{if(!this._gone&&current===this._generation)this.setData({loading:false});}},
 async change(e){if(this.data.busy||this._gone)return;const card=this.data.cards.find(c=>c.card_no===e.currentTarget.dataset.card),status=e.currentTarget.dataset.status;if(!card||!['lost','unbound'].includes(status))return;const operation=beginOperation(this,app,'busy');if(!operation)return;this.setData({error:''});try{const yes=await new Promise(resolve=>wx.showModal({title:status==='lost'?'挂失此卡？':'解绑此卡？',content:'已有订单仍可在小程序结束。解绑不转移钱包余额；恢复或绑定请由管理员核验。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));if(!yes||!operationVisible(this,app,operation))return;await app.request('POST','/user/cards/'+encodeURIComponent(card.card_no)+'/status',{status});if(operationVisible(this,app,operation))await this.load();}catch(error){if(operationVisible(this,app,operation))this.setData({error:error.message});}finally{finishOperation(this,operation);}}
});
