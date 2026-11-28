import { defineController, getApp, wx } from "../../runtime";
// 首页按服务端默认顺序分页展示公开站点。
const app = getApp();

// 进行中订单的服务端状态原文展示；未列出的状态直接回落为"处理中"，
// 不在客户端推断充电是否真的开始。
const ONGOING_LABELS = {
  pending_payment: "等待支付确认",
  paid: "已支付，设备启动确认中",
  charging: "正在充电",
};

export default defineController({
  _listGeneration: 0,
  data: {
    ongoing: null,
    announcements: [],
    stations: [],
    page: 0,
    stationLoading: false,
    moreStations: false,
    stationError: '',
  },

  onShow() {
    this.refresh();
    if (!this.data.stations.length) this.loadStations();
  },

  onPullDownRefresh() { return this.loadStations().finally(() => wx.stopPullDownRefresh()); },

  async refresh() {
    try {
      const ann = await app.request('GET', '/user/announcement/list', undefined, false);
      this.setData({ announcements: (ann.items || []).slice(0, 3).map(item => ({ id: item.id, title: item.title || '未命名公告' })) });
    } catch (_error) { this.setData({ announcements: [] }); }
    if (!app.globalData.token) { this.setData({ ongoing: null }); return; }
    try {
      const ong = await app.request('GET', '/user/charge/ongoing');
      // 服务端无进行中订单时返回空串而不是 null；两种情况都收起横幅。
      this.setData({ ongoing: ong?.order_no ? { ...ong, statusLabel: ONGOING_LABELS[ong.status] || "处理中" } : null });
    } catch (_error) { this.setData({ ongoing: null }); }
  },

  // 连续触底共用一次在途请求，失败后保留列表，由重试入口续读原页。
  onReachBottom() {
    if (this.data.moreStations && !this.data.stationLoading && !this.data.stationError) return this.loadStations(true);
  },
  retryStations() { return this.loadStations(this.data.page > 0); },

  // 下拉刷新重新读取第一页，丢弃刷新前发出的旧分页响应。
  async loadStations(append = false) {
    const generation = ++this._listGeneration;
    const page = append ? this.data.page + 1 : 1;
    const query = { page, page_size: 20 };
    this.setData({ stationLoading: true, stationError: '', ...(append ? {} : { stations: [], page: 0, moreStations: false }) });
    try {
      const result = await app.request('GET', '/user/station/nearby', query, false);
      if (generation !== this._listGeneration) return;
      const rows = result.items.map(station => {
        // 站点主键可能超过安全整数。封面只取末位分配示意图，导航保留完整十进制编号。
        const id = station.id_text ?? station.id;
        return { ...station, id, coverIndex: Number(String(id).slice(-1)) % 3 };
      });
      const seen = new Set(this.data.stations.map(station => station.id));
      this.setData({
        stations: this.data.stations.concat(rows.filter(station => !seen.has(station.id))),
        page, moreStations: rows.length === query.page_size,
      });
    } catch (error) {
      if (generation === this._listGeneration) this.setData({ stationError: error.message || '站点读取失败' });
    } finally {
      if (generation === this._listGeneration) this.setData({ stationLoading: false });
    }
  },

  goAnnouncements() { wx.navigateTo({ url: '/pages/announcement/list' }); },
  goOrders() { wx.navigateTo({ url: '/pages/charge/history' }); },
  goProfile() { wx.reLaunch({ url: '/pages/profile/profile' }); },
  goOngoing() {
    if (this.data.ongoing) {
      wx.navigateTo({ url: `/pages/charge/charging?order_no=${encodeURIComponent(this.data.ongoing.order_no)}` });
    }
  },

  goStation(event) {
    const id = event.currentTarget.dataset.id;
    wx.navigateTo({ url: '/pages/stations/detail?id=' + encodeURIComponent(id) });
  },
});
