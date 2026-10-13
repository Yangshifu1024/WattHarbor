import { defineController, getApp, wx } from "../../runtime";
import { newRequestId } from '../../utils/payment';
import { beginOperation, operationVisible, finishOperation, syncOperations } from '../../utils/page-operation';
const invoiceApp = getApp();
export default defineController({
  data: { orders: [], orderLabels: [], orderIndex: -1, selected: null, invoiceTypeIndex: 0, invoiceTypes: ['普通发票', '增值税专用发票'], title: '', taxNo: '', email: '', loading: false, loadingDetail: false, submitting: false, needsLogin: false, error: '', notice: '' },
  onLoad() { this._gone = false; this._generation = 0; this._session = invoiceApp._generation; this.loadOrders(); },
  onShow() {
    this._gone = false;
    const changed = this._session !== invoiceApp._generation;
    this._session = invoiceApp._generation;
    syncOperations(this, invoiceApp, ['submitting']);
    if (changed) {
      this._invoiceRequest = null;
      this._invoiceRequestId = null;
      this._generation++;
      this.setData({ orders: [], orderLabels: [], orderIndex: -1, selected: null, title: '', taxNo: '', email: '', error: '', notice: '', loadingDetail: false });
    }
    if (changed || this._wasHidden) {
      this.setData({ loadingDetail: false });
      if (!changed && this._invoiceRequest) this.setData({ notice: '申请结果请到发票记录核实；未确认时可重试同一申请。' });
      this.loadOrders();
    }
    this._wasHidden = false;
  },
  onHide() { this._gone = true; this._wasHidden = true; this._generation++; },
  onUnload() { this._unloaded = true; this.onHide(); },
  async login() { try { await invoiceApp.login(); if (!this._gone) await this.loadOrders(); } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  async loadOrders() {
    const generation = ++this._generation;
    if (!invoiceApp.globalData.token) { this.setData({ needsLogin: true, loading: false, orders: [], orderLabels: [] }); return; }
    this.setData({ loading: true, needsLogin: false, error: '' });
    try {
      const result = await invoiceApp.request('GET', '/user/charge/history', { page: 1, page_size: 100 });
      if (this._gone || generation !== this._generation) return;
      const orders = (result.items || []).filter(order => ['completed','refunding','refunded'].includes(order.status) && Number(order.total_cents??order.total_fee_cents)>0);
      this.setData({ orders, orderLabels: orders.map(order => `${order.order_no} · ${order.device_id} · ¥${((order.total_cents??order.total_fee_cents) / 100).toFixed(2)}`) });
      if (orders.length === 100) this.setData({ error: '仅显示最近 100 笔已完成订单；请选择需要开票的订单。' });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '订单读取失败', needsLogin: !invoiceApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  onOrderSelect(event) {
    const index = Number(event.detail.value);
    const order = this.data.orders[index];
    this.setData({ orderIndex: index, selected: null, error: '', notice: '' });
    if (!order) return;
    this.loadOrderDetail(order);
  },
  async loadOrderDetail(order) {
    const generation = ++this._generation;
    this.setData({ loadingDetail: true, selected: null, error: '' });
    try {
      const detail = await invoiceApp.request('GET', `/user/charge/${encodeURIComponent(order.order_no)}`);
      if (this._gone || generation !== this._generation) return;
      const paid = Number(detail.total_cents??detail.total_fee_cents);
      if (!['completed','refunding','refunded'].includes(detail.status) || !Number.isSafeInteger(paid) || paid <= 0) {
        throw new Error('该订单尚未完成计费或费用为零，暂不能申请发票');
      }
      this._invoiceRequestId = newRequestId();
      this._invoiceRequest = null;
      this.setData({ selected: { order_id: String(order.order_id), order_no: order.order_no, amount_cents: paid, amountText: (paid / 100).toFixed(2) } });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '订单详情读取失败' }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loadingDetail: false }); }
  },
  onType(event) { this.setData({ invoiceTypeIndex: Number(event.detail.value) }); },
  onTitle(event) { this.setData({ title: event.detail.value }); },
  onTaxNo(event) { this.setData({ taxNo: event.detail.value }); },
  onEmail(event) { this.setData({ email: event.detail.value }); },
  async submit() {
    if (this.data.submitting || this._gone || !this.data.selected) return;
    const title = this.data.title.trim();
    const taxNo = this.data.taxNo.trim();
    const email = this.data.email.trim();
    const invoiceType = this.data.invoiceTypeIndex === 1 ? 'vat_special' : 'normal';
    if (!title || title.length > 128) { this.setData({ error: '请填写有效的发票抬头（最多 128 字）' }); return; }
    if (taxNo.length > 32) { this.setData({ error: '税号最多 32 字符' }); return; }
    if (invoiceType === 'vat_special' && !taxNo) { this.setData({ error: '增值税专用发票必须填写税号' }); return; }
    if (email && (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) || email.length > 128)) { this.setData({ error: '邮箱格式无效' }); return; }
    const operation = beginOperation(this, invoiceApp, 'submitting');
    if (!operation) return;
    this.setData({ error: '', notice: '' });
    try {
      if (!this._invoiceRequest || this._invoiceRequest.session !== invoiceApp._generation) this._invoiceRequest = { session: invoiceApp._generation, body: {
        request_id: this._invoiceRequestId || (this._invoiceRequestId=newRequestId()), order_no: this.data.selected.order_no,
        invoice_type: invoiceType, title, tax_no: taxNo, email,
      } };
      const response = await invoiceApp.request('POST', '/user/invoice/apply', this._invoiceRequest.body);
      if (!operationVisible(this, invoiceApp, operation)) return;
      if (!response || !response.invoice_no) throw new Error('申请结果未确认，请到发票记录核实');
      this.setData({ notice: `发票申请已提交，编号 ${response.invoice_no}` });
      wx.showModal({ title: '申请已提交', content: `申请编号：${response.invoice_no}`, showCancel: false, success: () => { if (!this._gone && !this._unloaded && operation.session === invoiceApp._generation) wx.navigateBack(); } });
    } catch (error) { if (operationVisible(this, invoiceApp, operation)) this.setData({ error: error.message || '发票申请失败' }); }
    finally { finishOperation(this, operation); }
  },
});
