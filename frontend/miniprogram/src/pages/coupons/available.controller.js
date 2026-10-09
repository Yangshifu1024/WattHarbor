import { defineController, getApp, wx } from "../../runtime";
const couponApp = getApp();

// 支付前可用券：只读展示服务端判定的当前可用券。
// 后端未提供用户领取端点，发放由活动自动发放与后台完成，本页不受理领取。
// discount_type 表示立减、折扣或赠送时长；接口不返回优惠金额，页面不自行估算。
const typeLabels = { amount: '立减券', percentage: '折扣券', time_free: '免费时长券' };
function view(item) {
  return { ...item, typeText: typeLabels[item.discount_type] || item.discount_type,
    minimumText: ((item.min_charge_cents || 0) / 100).toFixed(2),
    expiresText: item.expires_at ? new Date(item.expires_at).toLocaleString('zh-CN', { hour12: false }) : '长期有效' };
}
export default defineController({
  data: { items: [], loading: false, error: '', needsLogin: false },
  onShow() { this._gone = false; this._generation = (this._generation || 0) + 1; this.load(); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  onPullDownRefresh() { this.load().finally(() => wx.stopPullDownRefresh()); },
  async login() { try { await couponApp.login(); if (!this._gone) await this.load(); } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  async load() {
    const generation = ++this._generation;
    if (!couponApp.globalData.token) { this.setData({ items: [], loading: false, needsLogin: true, error: '' }); return; }
    this.setData({ loading: true, error: '', needsLogin: false });
    try {
      const result = await couponApp.request('GET', '/user/coupons');
      if (this._gone || generation !== this._generation) return;
      if (!result || !Array.isArray(result.items)) throw new Error('可用券响应异常');
      this.setData({ items: result.items.map(view) });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '可用券读取失败', needsLogin: !couponApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  goMine() { wx.navigateTo({ url: '/pages/coupons/my' }); },
  goScan() { wx.reLaunch({ url: '/pages/scan/scan' }); },
});
