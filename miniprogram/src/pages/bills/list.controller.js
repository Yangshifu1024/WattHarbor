import { defineController, getApp, wx } from "../../runtime";
import { formatBill } from '../../utils/bill';
const billApp = getApp();

// 账单列表：分页读取所属账单；无筛选条件，已读状态由 read_at 是否存在决定。
export default defineController({
  data: { items: [], total: 0, page: 0, loading: false, error: '', needsLogin: false },
  onLoad() { this._gone = false; this._generation = 0; this.load(true); },
  onUnload() { this._gone = true; this._generation++; },
  onPullDownRefresh() { return this.load(true).finally(() => wx.stopPullDownRefresh()); },
  onReachBottom() { if (!this.data.loading && this.data.items.length < this.data.total) return this.load(false); },
  async login() { try { await billApp.login(); if (!this._gone) await this.load(true); } catch (e) { if (!this._gone) this.setData({ error: e.message || '登录失败，请重试' }); } },
  open(event) {
    const id = event.currentTarget.dataset.id;
    if (id) wx.navigateTo({ url: '/pages/bills/detail?id=' + encodeURIComponent(id) });
  },
  // 列表不代替详情页写已读凭据：只有打开详情才标记，避免误触即丢失未读。
  async load(reset) {
    if (!billApp.globalData.token) { this.setData({ needsLogin: true, loading: false, items: [], total: 0 }); return; }
    const generation = ++this._generation;
    const page = reset ? 1 : this.data.page + 1;
    this.setData({ loading: true, error: '', needsLogin: false, ...(reset ? { items: [], page: 0, total: 0 } : {}) });
    try {
      const result = await billApp.request('GET', '/user/bills', { page, page_size: 20 });
      if (this._gone || generation !== this._generation) return;
      if (!result || !Array.isArray(result.items) || !Number.isSafeInteger(result.total)) throw new Error('账单列表响应异常');
      this.setData({ items: (reset ? [] : this.data.items).concat(result.items.map(formatBill)), total: result.total, page });
    } catch (e) { if (!this._gone && generation === this._generation) this.setData({ error: e.message || '账单加载失败', needsLogin: !billApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
});
