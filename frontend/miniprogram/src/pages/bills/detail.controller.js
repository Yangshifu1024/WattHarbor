import { defineController, getApp, wx } from "../../runtime";
import { formatBill } from '../../utils/bill';
const billApp = getApp();

// 账单详情：按用户归属读取单张账单，并在成功展示后写已读凭据。
// 读取与已读是两个独立事实：账单读不到时不写已读；已读保存失败保留详情并单独提示。
export default defineController({
  data: { bill: null, loading: false, error: '', needsLogin: false, readSaving: false, readNotice: '', readError: '' },
  onLoad(query) {
    this._id = typeof query.id === 'string' && /^[1-9]\d{0,19}$/.test(query.id) ? query.id : '';
    this._gone = false; this._generation = 0;
    this.load();
  },
  onUnload() { this._gone = true; this._generation++; },
  onPullDownRefresh() { return this.load().finally(() => wx.stopPullDownRefresh()); },
  async login() { try { await billApp.login(); if (!this._gone) await this.load(); } catch (e) { if (!this._gone) this.setData({ error: e.message || '登录失败，请重试' }); } },
  async load() {
    if (!this._id) { this.setData({ error: '缺少账单标识', bill: null }); return; }
    if (!billApp.globalData.token) { this.setData({ needsLogin: true, bill: null, loading: false }); return; }
    const generation = ++this._generation;
    this.setData({ loading: true, error: '', needsLogin: false, bill: null });
    try {
      const bill = await billApp.request('GET', '/user/bills/' + encodeURIComponent(this._id));
      if (this._gone || generation !== this._generation) return;
      if (!bill || !bill.id) throw new Error('账单详情响应异常');
      // 已确认的已读状态不被旧响应覆盖，即使旧响应的 read_at 仍为空。
      const view = formatBill(bill);
      if (this._readBillID === bill.id && this._readConfirmed) view.unread = false;
      this.setData({ bill: view });
      // 账单已渲染后再等待已读凭据：保存失败不影响展示，读取与已读仍是两个独立事实。
      if (!bill.read_at && bill.read !== true) await this.markRead();
    } catch (e) { if (!this._gone && generation === this._generation) this.setData({ error: e.message || '账单详情加载失败', needsLogin: !billApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  // 同一账单只允许一个已读请求在途，成功后立即显示已读，不受页面加载版本影响。
  // 失败时清除在途标记，下次加载可重试。
  markRead() {
    if (this._readBillID === this._id && (this._readPending || this._readConfirmed)) return this._readPending;
    this._readBillID = this._id;
    this._readConfirmed = false;
    this.setData({ readSaving: true, readError: '', readNotice: '' });
    this._readPending = billApp.request('POST', '/user/bills/' + encodeURIComponent(this._id) + '/read', {}).then((result) => {
      if (!result || result.read !== true) throw new Error('已读状态未确认');
    }).then(() => {
      this._readPending = null;
      if (this._readBillID !== this._id || this._gone) return;
      this._readConfirmed = true;
      this.setData({ readSaving: false, readNotice: '已标记为已读', bill: { ...this.data.bill, unread: false } });
    }).catch((e) => {
      this._readPending = null;
      if (this._readBillID !== this._id || this._gone) return;
      this.setData({ readSaving: false, readError: e.message || '已读状态保存失败，可下拉重试' });
    });
    return this._readPending;
  },
  orderDetail() { if (this.data.bill?.order_no) wx.navigateTo({ url: '/pages/charge/detail?order_id=' + encodeURIComponent(this.data.bill.order_no) }); },
});
