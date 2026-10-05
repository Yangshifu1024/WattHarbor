# 用户端开发与交互优化路线图

状态：U1、U2、U3、U5、U9、U10、U12 已于 2026-10-06 实施并通过验证；U4、U6、U7、U8、U11 未开工。U5 实施时订正了原描述（后端无用户领取端点），详见第 2 节该行。验证方式与结果见第 5 节。

依据为 2026-10-06 对 `miniprogram/` 全部源码（Taro 4.3 + React 18，23 个注册页面，73 项用户端测试全绿）与 [user.openapi.json](../internal/http/user/user.openapi.json) 端点清单的逐项盘点。

本文件负责用户端 `miniprogram/` 的后续开发任务、优先级、依赖与验收；仓库维护规则、目录结构与代码约束见 [AGENTS.md](../AGENTS.md)，本地运行与联调见 [README](../README.md#本地运行)。完成任务后原位更新状态与证据，不追加第二份"当前状态"。

## 1. 现状结论

- **技术形态**：Taro 4.3 + React 18 + taro-ui 3.4，双端构建（weapp + h5）。自研运行时层 `src/runtime`：`controller.ts` 把小程序 Page 生命周期桥接进 React 函数组件，`session.js` 管令牌轮换与 401 自动刷新重放（generation 防串号），`platform.ts` 按 `TARO_ENV` 适配双端差异（H5 开发登录与模拟支付，weapp 真实微信能力）。
- **已覆盖业务**：扫码解析 → 选端口 → 套餐 → 支付启动（幂等 request_id、checkout 缓存、取消与中断恢复）、充电中快照轮询与近 30 分钟曲线、停止充电、订单历史／详情／补付／评价、设备报修与处理进度、钱包余额／充值挂起恢复／退款与复核状态／流水、在线卡挂失解绑、我的优惠券、发票申请与记录、公告、手机号绑定解绑。支付与退款的关键竞态防护（沿用原请求标识、不确定结果不重提）已实现。
- **与后端契约的缺口**：
  - 结算账单完全未接：`GET /user/bills`、`GET /user/bills/{id}`、`POST /user/bills/{id}/read` 已注册（`internal/http/user/settlement/bill.go`），小程序零调用。
  - 优惠券闭环断在中间：`GET /user/coupons`（可用券）未接；且下单链路全栈不支持用券——`marketing/rules_coupon_discount.go` 的 `discountFor` 无任何消费者，`/user/scan/offers`、`/user/scan/start` 契约无券字段。现状是用户可领券、可看券，但无法使用。
  - 首页 `index.controller.js` 已拉取进行中订单（`/user/charge/ongoing`）与公告（`/user/announcement/list`），但 `index.tsx` 两者均未渲染。
- **明确占位**：首页"常用电站"与站点详情"设备快捷充电"均为 toast 占位；首页"附近电站"与"设备地图"两个入口指向同一页面。
- **视图形态**：各页面 `.tsx` 为工具生成风格（`!!(x) &&` 超长单行、dataset 事件桥），控制器 `.controller.js` 为无类型 JS，类型契约仅 `runtime/models.ts` 三个接口；页面清单在 `src/app.config.ts` 与 `src/app-manifest.json` 双份维护。

## 2. 任务与优先级

规模定义：S ≈ 半天，M ≈ 1–2 天，L ≈ 3 天以上。任务相互独立可单独交付；标注"依赖后端"的任务须先确认后端契约或路线图排期，前端不得自行假设字段。

### P0 功能闭环

| 编号 | 任务 | 内容与边界 | 依赖 | 规模 | 验收 |
|---|---|---|---|---|---|
| U1 | 首页进行中订单与公告条 | **已完成。** `index.tsx` 渲染电中横幅（订单号、设备、端口，点击进 `charge/charging`）与公告条；数据源沿用控制器已拉取的 `ongoing` 与 `announcements`，未新增请求。`order_no` 为空串时收起横幅（服务端无进行中订单即返回空串）；公告最多取 3 条，其余由公告页查看 | 无 | S | 见第 5 节；控制器行为由 `tests/home-banners.test.cjs` 覆盖 |
| U2 | 支付完成后直达充电页 | **已完成。** `scan-result` 支付成功后 `reLaunch` 至 `charge/charging?order_no=<merchant_order_no>`，由快照轮询消化"启动确认中"；订单历史改为次级入口。页离开再返回时经 `_openChargingOnShow` 续跳 | 无 | S | 既有 `tests/scan.test.cjs` 断言已按新目标更新；`tests/orders.test.cjs` 补付路径原本已跳充电页 |
| U3 | 结算账单页面 | **已完成。** 新增 `pages/bills/list` 与 `pages/bills/detail`，接入列表、详情与 `POST /user/bills/{id}/read`。读取与已读是两个独立事实：已读回执保存失败不撤销已展示内容，且释放单飞允许重试；已确认的已读不被后续陈旧 `read_at` 退回。列表页不写已读凭据，只有打开详情才标记。入口挂个人中心与订单详情 | 无 | M | `tests/bills.test.cjs` 7 项覆盖分页、未读判定、404/503 语义与重试；`/user/bills` 未提供按订单号筛选，订单详情跳转只进列表不传无效参数 |
| U4 | 下单用券 | **未开工，后端契约先行。** 后端先行：`/user/scan/offers` 响应携带该端口可用券预览，`/user/scan/start` 请求携带 `coupon_grant_id`，结算接线 `discountFor` 并明确部分退款时券的分摊规则（首版建议：整单退时券全额退回，未消费部分不返还）；同步 `user.openapi.json` 与契约测试。前端随后：选券弹层、已选券金额展示、券不可用原因提示 | 后端契约 | L | 待办 |
| U5 | 支付前可用券页面 | **已完成。原描述订正**：后端 `GET /user/coupons` 是只读的"当前可用于下单的券"查询，`internal/http/user/marketing/api.go` 未注册任何用户领取端点，发放由活动自动发放与后台完成。页面据此改为只读展示，不受理领取、不发起任何写入；该读模型不返回优惠金额，页面不推算可省多少。入口挂"我的优惠券" | 无 | M | `tests/home-banners.test.cjs` 断言本页不产生 POST；`tests/complete-pages.test.cjs` 覆盖路由注册 |

### P1 体验杠杆

| 编号 | 任务 | 内容与边界 | 依赖 | 规模 | 验收 |
|---|---|---|---|---|---|
| U6 | 导航栈与原生 tabBar | 配置原生 tabBar（首页 / 扫码 / 我的，保活不丢栈），普通页面跳转改回 `navigateTo` 保留返回栈；订单详情、充电状态、scan-result 等过程页移除自定义 TabBar；`runtime/TabBar.tsx` 相应收缩或删除。需回归全部页面跳转路径与 `session.js` 的 `reLaunch` 场景 | 无 | M | 三 tab 切换不销毁栈；详情页可返回上级；微信开发者工具与 H5 双端跳转路径无死链 |
| U7 | 充电页可视化 | 电量进度环与预计结束时间；功率曲线图形化（weapp 用 canvas 2d 自绘或评估 echarts-for-weapp，H5 同源方案）；曲线随快照轮询自动刷新，保留手动刷新；单位与"暂无数据"占位沿用现行 `display` 契约，不伪造遥测 | 无 | M | 有遥测 / 无遥测 / 已结束三种状态展示正确；数据缺失不显示零值；双端渲染核对 |
| U8 | 微信订阅消息 | 启动确认、充电完成、退款到账、发票审核结果四类通知。前端只做订阅授权引导与跳转；事件外发依赖后端 Webhook 投递装配（当前 worker 调度器处理器登记表为空，属重构路线图遗留项），须后端先落地 | 后端投递能力 | L | 授权流程不阻塞主链路；通知触达以真实微信端为准，模拟链路不作为验收 |

### P2 工程质量

| 编号 | 任务 | 内容与边界 | 依赖 | 规模 | 验收 |
|---|---|---|---|---|---|
| U9 | 页面清单单一来源 | **已完成。** `src/app-manifest.json` 已删除（此前已与 `app.config.ts` 漂移：多出一个 `tabBar` 段），`tests/complete-pages.test.cjs` 改为直接求值 `app.config.ts`，并加一条测试防止副本回流 | 无 | S | 完整性测试仍能发现缺 tsx / config / controller 的页面；副本存在即测试失败 |
| U10 | 下拉刷新按页声明 | **已完成。** 移除 `window.enablePullDownRefresh` 全局开关；`profile/profile`、`wallet/wallet`、`wallet/txns`、`wallet/refund` 补上声明，`scan/scan` 移除"配了却无 handler"的声明。构建产物核对：16 个实现 handler 的页面全部声明，无一多配 | 无 | S | 以 `dist/weapp` 各页 json 与控制器源码逐页比对核对 |
| U11 | tsx 可读性方向定案 | **未开工。** 现有 tsx 为生成物且无生成器入库，手改成本高。二选一并记录于本文件：把生成器入库使其可再生，或后续改版时逐步重写为可读 JSX。禁止停在"不敢手改也无法再生成"的状态 | 无 | M | 待办 |
| U12 | 运行时类型与清理 | **已完成。** `runtime/models.ts` 补 `OngoingCharge`、`BillRow`、`RedeemableCoupon`；删除遗留空目录 `pages/` 与 `utils/`；`user.openapi.json` 的 `OngoingChargeResult` 补齐处理器实际返回的 `device_id`、`port_no`、`started_at`、`ended_at`（此前只声明两个字段，与实现不符） | 无 | S | `npm run typecheck` 通过；OpenAPI 字段与 `internal/http/user/order/query.go` 实际响应逐项对齐 |

## 3. 交互优化清单

以下各项随对应页面改版逐项挑用，不设独立排期；与上表任务重叠的项（首页横幅、支付直达、曲线自动刷新）以上表为准。

- **首页**：合并"附近电站 / 设备地图"重复入口，腾位给"常用电站"（需后端提供最近使用站点查询）；公告条与进行中订单横幅见 U1。
- **扫码 / scan-result**：支付成功卡片增加"查看充电状态"主按钮（配合 U2）；"重新扫码"在已尝试启动后移出主路径，避免误触。
- **充电中**：顶部进度与预计结束、曲线自动刷新见 U7；订单结束后提供"去开发票 / 去评价"的后续引导，而非只有订单详情入口。
- **列表页（订单 / 流水 / 优惠券）**：状态筛选由 Picker 下拉改为分段控件；统一"触底自动加载 + loading 尾部提示"，移除残留的手动"加载更多"按钮（发票记录页的 `onReachBottom` 条件与其他页不一致，一并核对）。
- **钱包充值 / 退款**：增加快捷金额按钮；充值挂起单、退款复核状态的展示文案保持现有不冒称到账的措辞。
- **发票**：申请页对"仅最近 100 笔订单"的边界提示前置到选择器上方；发票文件由复制链接升级为 `wx.downloadFile` + `openDocument` 直接预览（H5 退回复制链接）。
- **H5 端**：`stations/nearby` 的经纬度手动输入框收进"定位失败"展开区，收敛调试观感；文字地图、手动输码等 H5 降级维持现状并在页内说明。
- **全局**：错误提示统一卡片 + 重试模式（现状已基本一致，改版时不引入新的弹层样式）；骨架屏可在列表首屏评估，不强制。

## 4. 验证方式

- 每项交付前执行：`npm run typecheck`、`npm test`、`npm run build:h5` 与 `npm run build:weapp`；新增或修改控制器行为时按 `tests/*.test.cjs` 既有模式补测试（源码变换 + vm 运行 + 假 app/wx 断言）。
- UI 变更访问容器内用户端 H5 地址并核对页面控制台无报错；联调顺序与地址见 [README](../README.md#本地运行)。微信端特有能力（支付、扫码、订阅消息、手机号授权）以微信开发者工具与实机为准，模拟链路不作为验收。
- 接入 U3、U5 时核对 `user.openapi.json` 已声明的账单与可用券端点；U4 涉及契约变更，后端与文档同步修改后前端再接入，不按假设字段开发。

## 5. 2026-10-06 实施与验证记录

本轮实施 U1、U2、U3、U5、U9、U10、U12；U4、U6、U7、U8、U11 未开工。

### 5.1 检查与构建

- `npm run typecheck` 通过；`npm test` **73 项通过 0 失败**（原 59 项 + 新增 14 项：`tests/bills.test.cjs` 7 项、`tests/home-banners.test.cjs` 6 项、页面清单单源 1 项）。
- `npm run build:h5` 与 `npm run build:weapp` 均成功；`dist/weapp/app.json` 登记 23 页，新增 `pages/bills/{list,detail}` 与 `pages/coupons/available` 产物齐全。
- 下拉刷新对齐以构建产物逐页核对：实现 handler 的 16 页全部声明，无多配，`window.enablePullDownRefresh` 已为 `undefined`。

### 5.2 开发栈活体验证

在 `compose.dev.yaml` 开发栈、开发登录模式（`tester-1`）下经浏览器实际渲染核对：

- 首页：无进行中订单与生效公告时横幅与公告条均正确收起，四个功能入口与附近电站区块布局完好。
- 账单列表：登录态下正确显示空态"暂无结算账单"，页面与底部导航渲染正常。
- 支付前可用券：正确显示空态与"查看我的全部优惠券 / 去扫码充电"入口；点击"去扫码充电"确实落到 `pages/scan/scan`（原 `coupons/my` 的同名按钮此前 reLaunch 到首页，已一并订正）。
- 个人中心："我的账单"入口可见并可进入。

**未覆盖**：首页横幅与公告条的**有数据**形态需真实进行中订单与生效公告，本轮未构造，相关断言由 `tests/home-banners.test.cjs` 覆盖数据条件（空 `order_no` 收起、`paid` 显示"启动确认中"、公告上限 3 条）。

**环境注意**：改动 `app.config.ts` 新增页面后，dev 容器的 Taro dev server 只对 `.tsx` 做 HMR，**不会重建路由表**；新页面表现为 URL 已切换但仍渲染旧页面。必须 `docker compose -f compose.dev.yaml restart user-h5` 后新路由才可用。本轮首次测试即因此判为环境未重载，重启后重测通过。

**工具限制**：本次浏览器运行时未提供控制台读取能力，无法收集 console 日志；改以页面可见报错形态（错误文案、白屏、布局破损）判断，各页未见异常。
