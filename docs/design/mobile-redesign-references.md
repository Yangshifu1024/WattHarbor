# 充电 App 原图对照与电港改版取舍

以下参考包括设计师公开作品、开源项目和充电网络发布的产品界面。开源实现与商业 App 分列，图中站点、价格、状态和版本均属于来源产品。点原图可放大。[电港交互样稿](mobile-redesign-preview.html) · [改版提案](mobile-redesign-proposal.md)

## 设计网站上的充电 UI

五套候选的颜色与版式由电港自行组合。设计网站作品用于观察构图与任务顺序，运营方来源用于核对现场动作。下列案例均可直接查看作品页。

| 作品与作者 | 来源中可核对的内容 | 对应的电港候选 |
| --- | --- | --- |
| Arounda Product 的 EV Charging | 作者突出单一充电读数，费用、计量和停止操作作为次级信息。作品页列有朱红、桃橙等色值。[Dribbble 作品](https://dribbble.com/shots/27863814-UI-UX-Design-Mobile-App-Design-for-EV-Charging) | A 暖橙车棚借用暖色与单一视觉重点，车辆主图是电港原创生成图。 |
| Zahid Hasan / Crevio 的 Dark UI Concept | 作者为夜间充电设计暗色 UI，作品色板列有近黑、灰蓝和亮蓝。[Dribbble 作品](https://dribbble.com/shots/27391220-EV-Charging-App-Dark-UI-Concept) | B 午夜仪表将已识别插座放在圆形读数中央，套餐面板由用户展开。 |
| Vijay C、aspira design 等的 Power Booker | 作者展示 EV 充电时段选择与预约应用。[Behance 作品](https://www.behance.net/gallery/191637767/Power-Booker-EV-Charging-Slot-Booking-Application) | D 柔紫分步将需要做的选择按顺序呈现。电港只演示当前插座和预付额。 |
| Ashutosh Ray 的 Autocharz | 作者说明其设计重点为简洁的信息层级、实时可用性与充电会话管理。[Behance 作品](https://www.behance.net/gallery/245238465/Autocharz-EV-Charging-App-UIUX-Design-Case-Study) | D 减少每屏选择；E 黑白收据强调费用与状态连续可查。收据外观是电港的设计提案。 |

C 黄黑桩牌另参考 Fastned 官方演示与慧知的端口选择实图，用实物号和状态矩阵服务多插座车棚。[Fastned 官方演示第 42 页](https://www.fastnedcharging.com/media/lozn01dc/chargingdaypresentation-pdfversion3.pdf#page=42) · [慧知端口选择](https://github.com/roinli/HUIZHI-ChargeOS-mini/blob/master/assets/hcp-cloud/hcp-mini/选择充电.png)

## 四张图看清四个设计问题

| 出发前找站 · IONITY | 在桩前选口 · 慧知 | 返回首页查单 · ad-car-Charge | 离开后看进度 · Electrify America |
| --- | --- | --- | --- |
| [![IONITY 站点地图与余量](https://cdn.prod.website-files.com/66e197644ead7b648dbae27d/69981b9d03101c8b08f174c2_ionity_app_locations.webp)](https://www.ionity.eu/app) | [![慧知端口和时长选择](https://raw.githubusercontent.com/roinli/HUIZHI-ChargeOS-mini/master/assets/hcp-cloud/hcp-mini/选择充电.png)](https://github.com/roinli/HUIZHI-ChargeOS-mini/blob/master/assets/hcp-cloud/hcp-mini/选择充电.png) | [![ad-car-Charge 首页进行中订单](https://raw.githubusercontent.com/adsion/ad-car-Charge/main/images/首页-1.png)](https://github.com/adsion/ad-car-Charge/blob/main/images/首页-1.png) | [![Electrify America 会话进度](https://www.electrifyamerica.com/img/progress%402x.765443f9.png)](https://www.electrifyamerica.com/mobile-app/) |

## 国际充电网络的官方界面

| 产品 | 具体参考 | 电港可以学什么 |
| --- | --- | --- |
| IONITY | 站点图把选中站、距离和可用枪数放在地图上下文中，官方另有进行中会话画面。[站点原图](https://cdn.prod.website-files.com/66e197644ead7b648dbae27d/69981b9d03101c8b08f174c2_ionity_app_locations.webp) · [会话原图](https://cdn.prod.website-files.com/66e197644ead7b648dbae27d/67ebc702a50cc19a1d518c10_ionity_app-session_en.webp) · [App 图集](https://www.ionity.eu/app) | 站点列表可借鉴其单站摘要。拿不到可信的空闲统计时，站点行只给「查看设备」；具体可选插座在设备页确认。 |
| Electrify America | 官方展示找站和追踪充电两种视图，开始前要求核对桩身的 Charger ID。[找站原图](https://www.electrifyamerica.com/img/locate_a_charger.7ef63c0d.png) · [进度原图](https://www.electrifyamerica.com/img/progress%402x.765443f9.png) · [官方操作说明](https://www.electrifyamerica.com/mobile-app/) | 让桩身编号与屏幕上的编号一一对应。会话开启后，第一眼看到状态和可信计量；用户需要时再展开费用明细。 |
| Fastned | 官方展示稿把地图路线和正在充电摆在两部手机上；会话图将电量、功率和停止操作集中展示。该图为公开演示时的版本。[官方演示图第 42 页](https://www.fastnedcharging.com/media/lozn01dc/chargingdaypresentation-pdfversion3.pdf#page=42) · [现行 App 介绍](https://www.fastnedcharging.com/en/charging/fastned-app) | 借鉴一眼读完本次数据的排布。两轮车端用已充时长、实测电量、实测功率，缺遥测就显示「暂无数据」。 |
| Tesla | 其他品牌车辆的官方操作顺序为找到超级充电站、选择实际车位、启动。该来源用于核对交互顺序。[Tesla 官方使用流程](https://www.tesla.com/en_gb/support/charging/supercharging-other-evs) | 将「选站、核对实际插座、确认金额」放在付款前。扫码已定位到可用插座时直接进入核对。 |

## 有公开源码的充电产品

| 项目 | 作者原图与可观察到的布局 | 采用的细节 |
| --- | --- | --- |
| ad-car-Charge · MIT | 首页把进行中的订单放在活动区之前；附近电站可纵向比较；订单列表能看到各笔状态。[首页](https://github.com/adsion/ad-car-Charge/blob/main/images/首页-1.png) · [电站](https://github.com/adsion/ad-car-Charge/blob/main/images/电站-1.png) · [订单](https://github.com/adsion/ad-car-Charge/blob/main/images/订单-1.png) · [源码](https://github.com/adsion/ad-car-Charge) | 借用「回首页续查」；电港有订单时进一步将状态前置到首页首位。 |
| EVMap · 开源 Android 找站应用 | 全屏地图上布置站点标记；进入详情后才显示地址、接口、功率等。[地图](https://raw.githubusercontent.com/ev-map/EVMap/master/_img/screenshots/phone/en/mapbox/01_map.png) · [详情](https://raw.githubusercontent.com/ev-map/EVMap/master/_img/screenshots/phone/en/mapbox/02_detail.png) · [源码](https://github.com/ev-map/EVMap) | 采用站点摘要与设备详情分层，电港用列表选择站点。 |
| 土拨鼠 · Apache-2.0 | 微信小程序有纵向站点清单，订单逐笔显示状态、开始和结束时间。[站点列表](https://github.com/cheinlu/HarmonyOS-groundhog-charging-system/blob/master/md_img/xcx3.png) · [订单](https://github.com/cheinlu/HarmonyOS-groundhog-charging-system/blob/master/md_img/xcx2.png) · [源码](https://github.com/cheinlu/HarmonyOS-groundhog-charging-system) | 站点用纯列表浏览；保留站名、地址和设备入口。 |
| 慧知 ChargeOS-mini · 仓库公开，GitHub 未标注许可证 | 底部中央扫码；先选端口，再选时长或策略；状态图另有车辆电池图。[首页](https://github.com/roinli/HUIZHI-ChargeOS-mini/blob/master/assets/hcp-cloud/hcp-mini/主页.jpg) · [选端口与套餐](https://github.com/roinli/HUIZHI-ChargeOS-mini/blob/master/assets/hcp-cloud/hcp-mini/选择充电.png) · [充电中](https://github.com/roinli/HUIZHI-ChargeOS-mini/blob/master/assets/hcp-cloud/hcp-mini/正在充电.png) · [源码](https://github.com/roinli/HUIZHI-ChargeOS-mini) | 主操作集中在首屏。普通 H5 直接输入，小程序点开相机。多插座设备用编号矩阵，使用中或故障同时写明状态。 |

## 本轮采用的方向

前期配色候选已结束评审。当前[四页交互样稿](mobile-redesign-preview.html)按首页、充电站、设备、我的组织。借鉴案例中的站点摘要、实物端口核对与订单状态分层，采用石墨黑、金属银和冰蓝提示色。首页通过列表选站。
