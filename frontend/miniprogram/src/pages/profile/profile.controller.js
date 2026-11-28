import { defineController, getApp, wx } from "../../runtime";
// 个人中心
const app = getApp();

export default defineController({
  data: { profile: null, recentOrder: null, orderError: false, orderLoading: false, loading:false, error:'', needsLogin:false, loggingIn:false },
  _generation:0,
  _gone:false,
  onShow() { this._gone=false; this.setData({loggingIn:false}); this.refresh(); },
  onHide() { this._gone=true; this._generation++; },
  onUnload() { this.onHide(); },
  onPullDownRefresh() { this.refresh().finally(()=>wx.stopPullDownRefresh()); },
  async login() {
    if (this.data.loggingIn) return;
    this.setData({loggingIn:true,error:''});
    try { await app.login(); if (!this._gone) await this.refresh(); }
    catch(e     ) { if(!this._gone) this.setData({error:e.message || '登录失败'}); }
    finally { if(!this._gone) this.setData({loggingIn:false}); }
  },
  async refresh() {
    const generation=++this._generation;
    this.setData({profile:null,recentOrder:null,orderError:false,orderLoading:false,error:'',needsLogin:!app.globalData.token,loading:false});
    if (!app.globalData.token) return;
    this.setData({loading:true});
    try {
      /** @type {import('../../runtime/models').ChargeUserProfile} */
      const p = await app.request     ('GET', '/user/profile');
      if (!p || typeof p.user_id !== 'string' || !/^[1-9]\d{0,19}$/.test(p.user_id)) {
        throw new Error('用户编号响应格式不正确，请重新读取');
      }
      if (this._gone || generation!==this._generation) return;
      this.setData({profile:{...p,
        balanceText:(p.wallet.available_cents/100).toFixed(2),frozenText:(p.wallet.frozen_cents/100).toFixed(2),
        registeredText:p.registered_at.slice(0,10),
      }});
      this.loadRecentOrder(generation);
    } catch (e     ) {
      if(!this._gone && generation===this._generation) this.setData({error:e.message || '资料读取失败',needsLogin:!app.globalData.token});
    } finally { if(!this._gone && generation===this._generation) this.setData({loading:false}); }
  },
  // 摘要与钱包独立加载，切换账号或隐藏页面后丢弃旧请求结果。
  async loadRecentOrder(generation) {
    this.setData({orderLoading:true,orderError:false});
    try {
      const result = await app.request('GET', '/user/charge/history', {page:1,page_size:1});
      if (!this._gone && generation===this._generation) this.setData({recentOrder:result.items[0] || null});
    } catch {
      if (!this._gone && generation===this._generation) this.setData({orderError:true});
    } finally {
      if (!this._gone && generation===this._generation) this.setData({orderLoading:false});
    }
  },
  openRecentOrder() {
    const number = this.data.recentOrder?.order_no;
    if (number) wx.navigateTo({url:'/pages/charge/detail?order_id='+encodeURIComponent(number)});
  },
  onLogout() {
    wx.showModal({title:'退出登录?',success:async(r     )=>{
      if(!r.confirm) return;
      ++this._generation;
      this.setData({loading:false});
      try { await app.logout(); if(!this._gone) this.setData({profile:null,recentOrder:null,needsLogin:true,error:'',loading:false}); }
      catch(e     ) { if(!this._gone) this.setData({error:e.message || '退出失败，请重试'}); }
    }});
  },

  goPhoneBind() { wx.navigateTo({ url: '/pages/profile/phone' }); },
  goCards() { wx.navigateTo({ url: '/pages/wallet/cards' }); },
  goFault() { wx.navigateTo({ url: '/pages/dev/fault' }); },
  goWallet() { wx.navigateTo({ url: '/pages/wallet/wallet' }); },
  goBills() { wx.navigateTo({ url: '/pages/bills/list' }); },
  goRefunds() { wx.navigateTo({ url: '/pages/wallet/refund' }); },
  goOrders() { wx.navigateTo({ url: '/pages/charge/history' }); },
  goAnnouncements() { wx.navigateTo({ url: '/pages/announcement/list' }); },
  goCoupons() { wx.navigateTo({ url: '/pages/coupons/my' }); },
  goInvoices() { wx.navigateTo({ url: '/pages/invoice/my' }); },
});
