import { defineController, getApp, wx } from "../../runtime";
const orderApp = getApp();
import {formatWalletTxn} from '../../utils/wallet-txn';
export default defineController({
  data: { items: [], total: 0, page: 0, loading: false, error: '', needsLogin: false,
    statusIndex: 0, statusNames: ['全部','收入','支出'], statusValues: ['','in','out'] },
  onShow() { this._generation = (this._generation || 0)+1; this._gone = false; this.load(true); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this._gone = true; this._generation++; },
  onPullDownRefresh() { this.load(true).finally(() => wx.stopPullDownRefresh()); },
  onReachBottom() { if (!this.data.loading && this.data.items.length < this.data.total) this.load(false); },
  onStatus(event) { this.setData({ statusIndex: Number(event.detail.value) }); this.load(true); },
  async login() {
    try { await orderApp.login(); if(!this._gone) await this.load(true); }
    catch (e) { if(!this._gone)this.setData({ error: e.message || '登录失败，请重试' }); }
  },
  retry() { this.load(this.data.page === 0); },
  async load(reset) {
    const generation = ++this._generation;
    if (!orderApp.globalData.token) { this.setData({ needsLogin: true, loading: false, items: [], total: 0 }); return; }
    const page = reset ? 1 : this.data.page + 1;
    this.setData({ loading: true, error: '', needsLogin: false, ...(reset ? { items: [], page: 0, total: 0 } : {}) });
    try {
      const status = this.data.statusValues[this.data.statusIndex];
      const result = await orderApp.request('GET', '/user/wallet/txns', { page, page_size: 20, ...(status ? { direction:status } : {}) });
      if (this._gone || generation !== this._generation) return;
      this.setData({ items: (reset ? [] : this.data.items).concat(result.items.map(formatWalletTxn)), total: result.total, page });
    } catch (e) { if (!this._gone && generation === this._generation) this.setData({ error: e.message || '流水加载失败', needsLogin: !orderApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
});
