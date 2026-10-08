import { defineController, getApp, wx } from "../../runtime";
const stationApp = getApp();
export default defineController({
  data: { items: [], markers: [], latitude: null, longitude: null, loading: false, error: '', radiusIndex: 1, radii: [1,5,10,20,50], needsLogin: false, locationNotice: '' },
  onLoad() { this._generation=0; this._gone=false; },
  onShow() { this._gone=false; return this.locate(); },
  onHide() { this._gone=true; this._generation++; this.setData({loading:false}); },
  onUnload() { this._gone=true; this._generation++; },
  inputLatitude(event) { this.setData({latitudeInput:event.detail.value}); },
  inputLongitude(event) { this.setData({longitudeInput:event.detail.value}); },
  useCoordinates() {
    const latitude=Number(this.data.latitudeInput),longitude=Number(this.data.longitudeInput);
    if(!this.data.latitudeInput || !this.data.longitudeInput || !Number.isFinite(latitude)||Math.abs(latitude)>90||!Number.isFinite(longitude)||Math.abs(longitude)>180){this.setData({error:'请输入有效经纬度'});return;}
    this.setData({latitude,longitude,locationNotice:''});return this.load();
  },
  // 定位失败仍查询公开站点，无坐标时由服务端按最新站点排序。
  locate() {
    const generation=++this._generation;
    this.setData({error:'',locationNotice:'',loading:true});
    return new Promise(resolve=>wx.getLocation({ type:'gcj02', success:location=>{
      if(this._gone || generation!==this._generation){resolve();return;}
      this.setData({latitude:location.latitude,longitude:location.longitude,loading:false});
      this.load().finally(resolve);
    }, fail:()=>{
      if(this._gone || generation!==this._generation){resolve();return;}
      this.setData({latitude:null,longitude:null,loading:false,locationNotice:'未开启定位，以下按最新电站展示。'});
      this.load().finally(resolve);
    } }));
  },
  onRadius(event) { this.setData({radiusIndex:Number(event.detail.value)}); this.load(); },
  async load() {
    const generation=++this._generation;
    this.setData({loading:true,error:'',items:[],markers:[]});
    try {
      const query=this.data.latitude==null ? {} : {latitude:this.data.latitude,longitude:this.data.longitude,radius_km:this.data.radii[this.data.radiusIndex]};
      const result=await stationApp.request('GET','/user/station/nearby',query,false);
      if (this._gone || generation!==this._generation) return;
      this.setData({items:result.items.map(s=>({...s,distanceText:typeof s.distance_km==='number'?s.distance_km.toFixed(2)+' 公里':'距离未知'})),markers:result.items.map(s=>({id:s.id,latitude:s.latitude,longitude:s.longitude,title:s.name}))});
    } catch(e) { if (!this._gone && generation===this._generation) this.setData({error:e.message || '站点查询失败'}); }
    finally { if (!this._gone && generation===this._generation) this.setData({loading:false}); }
  },
  onPullDownRefresh() { this.load().finally(()=>wx.stopPullDownRefresh()); },
  open(event) { const id=event.currentTarget.dataset.id || event.detail.markerId; wx.navigateTo({url:'/pages/stations/detail?id='+encodeURIComponent(id)}); },
});
