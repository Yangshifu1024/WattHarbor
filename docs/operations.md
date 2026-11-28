# 运行与数据维护

以下命令从仓库根目录执行。快速启动及开发检查见 [README](../README.md)，业务恢复规则见 [业务与接口契约](business-contracts.md)。

## 配置

以 [config.go](../backend/internal/platform/config/config.go)、Compose 和前端构建配置为准。开发 Compose 提供本地值；生产从 `.env` 和显式环境传入。密钥、私钥和真实配置不提交。

| 变量 | 使用方及规则 |
| --- | --- |
| `DATABASE_URL` | 应用的设备库 URL；单库迁移使用此变量 |
| `DATABASE_URL_CENTRAL`、`DATABASE_URL_GATEWAY`、`DATABASE_URL_WORKER` | 角色数据库连接。生产指向同一个 `wattharbor` 单库，开发分别指向三库 |
| `APP_UNIFIED_DATABASE` | 生产设为 `true`，使用单库中独立的设备 Outbox；三库模式为 `false` |
| `APP_HTTP_ADDR`、`DC589_ADDR` | 单体 HTTP 和设备 TCP 监听，默认 `:8080`、`:9100` |
| `REDIS_CACHE_URL`、`REDIS_STREAM_URL` | 分别用于会话缓存和事件流 |
| `DB_PASSWORD`、`DB_APP_PASSWORD` | MySQL 维护账号和应用运行账号密码 |
| `JWT_SECRET` | 用户与后台会话签名，至少 32 字节 |
| `SERVICE_TOKEN` | 进程内接口共享令牌 |
| `DEVICE_MAX_CONNECTIONS` | gateway，正整数，默认 10000 |
| `GATEWAY_DEBUG_HEARTBEAT` | gateway 心跳日志，默认 false；开发 Compose 固定 true |
| `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD` | 仅 admin，空后台账号表的引导凭证 |
| `ADMIN_TRUSTED_PROXIES` | 仅 admin，逗号分隔的可信代理 IP／CIDR；默认空，非法值在路由装配时阻止启动。开发直连保留空值 |
| `ADMIN_PROXY_SUBNET`、`CADDY_ADMIN_PROXY_IP` | 生产 Compose 的专用代理网段及 Caddy 固定 IPv4，默认 `172.30.82.0/24` 与 `172.30.82.2`；后台仅信任这个单一地址 |
| `LOGIN_MODE` | 仅 user，默认 `wechat`；`development` 要求模拟支付 |
| `WECHAT_APPID`、`WECHAT_SECRET` | AppID 为 user / central 必填；Secret 仅 user 登录与手机号授权使用 |
| `PAYMENT_MODE` | user / central，`disabled/simulation/wechat_direct`，默认 disabled；生产 Compose 显式注入 |
| `WECHAT_MCH_ID`、`WECHAT_CERT_SERIAL`、`WECHAT_APIV3_KEY` | user / central 的微信直连商户 ID、证书序列号及 32 字节 API v3 密钥 |
| `WECHAT_PRIVATE_KEY_PATH`、`WECHAT_NOTIFY_URL` | 应用容器内私钥路径及对外支付回调地址；路径变量不代替私钥挂载 |
| `REGULATORY_MODE` | 预留模板／Compose 变量；当前 config 与 worker 不读取，修改值不会启用监管任务 |
| `REGULATORY_ENDPOINT`、`REGULATORY_SIGNING_SECRET` | 预留对接参数；当前 worker 未装配发送适配器，尚无已接线的平台字段、签名或响应契约 |
| `EXPORT_DIR` | admin 导出目录，生产已配置持久卷 |
| `VITE_API_BASE` | 后台 API 地址，构建时使用 |
| `TARO_APP_API_BASE` | 用户 API 前缀，默认 `/api/v1`；微信发布需绝对 HTTPS 地址 |
| `TARO_APP_LOGIN_MODE` | 用户构建登录模式，默认 wechat |
| `DEV_API_PROXY_TARGET` | 后台和 H5 的开发代理均指向单体 HTTP |

`.env.example` 还含未被当前实现或根 Compose 消费的 `TAG`、`ADMIN_BOOTSTRAP_PHONE`、`WECHAT_REFUND_NOTIFY_URL`、`WECHAT_CUSTOMER_SERVICE_CORP_ID`、`OTEL_EXPORTER_OTLP_ENDPOINT`、`DEFAULT_TIME_OF_USE_JSON`，修改这些值不会启用相应功能。

## 单体部署

开发和生产均运行 `app`，对应单体命令 `wattharbor`。生产 [compose.prod.yaml](../deploy/compose.prod.yaml) 构建内嵌后台与 H5 的镜像。复制 `.env.example` 为 `.env`，配置数据库维护和运行密码、独立 Redis 密码、JWT、服务令牌、微信配置及公开域名。可选的 `ADMIN_BOOTSTRAP_USER` 和 `ADMIN_BOOTSTRAP_PASSWORD` 用于空库首次创建后台管理员。

```sh
node scripts/ops/check-deploy.mjs
docker compose --env-file .env -f deploy/compose.prod.yaml up -d --build --wait
docker compose --env-file .env -f deploy/compose.prod.yaml exec app wget -qO- http://127.0.0.1:8080/health/ready
```

MySQL 就绪后，Goose 初始化 `wattharbor` 单库，随后配置运行账号。MySQL、运行账号配置和 Redis 就绪后，应用装配模块，绑定 HTTP 与 DC589 TCP 并启动后台任务。首次成功完成同步轮次且依赖检查通过后，`/health/ready` 返回 200。收到 SIGTERM 时先停止接收 HTTP 请求与设备连接，等待已接收请求后关闭设备会话和后台任务。应用关停上限为 15 秒，Compose 留出 20 秒。

Caddy 代理后台、H5、用户 API 和公开通知。默认固定代理地址为 `172.30.82.2`，调整时同时设置 `ADMIN_PROXY_SUBNET` 与 `CADDY_ADMIN_PROXY_IP`。内网从 `/metrics/{admin,user,central,gateway,worker}` 抓取各角色指标，前缀为 `wattharbor_`。配置检查核对数据库、Redis、网络及 Caddy 分流，并在临时容器中验证 Caddy 配置。从仓库根目录运行 `COMPOSE_ENV_FILES=.env.example node scripts/ops/check-deploy.mjs --config-only` 可用示例配置检查结构。

开发项目沿用 `wattharbor-dev` 和已有三库数据卷。`dev-up` 会移除该 Compose 项目的旧进程容器并保留数据卷。生产项目保留既有单体项目名 `wattharbor-monolith` 及其数据卷。从旧生产拓扑切换前，需停旧 Go 进程，核对备份、数据卷、应用运行账号和 Redis Stream。已有三库可用 `node tasks.mjs build wattharbor` 构建单体，配置原三库 URL 运行，其中 `DATABASE_URL` 指向 gateway 库，`APP_UNIFIED_DATABASE=false`。需要合库时先在备份环境核对业务表、两路 Outbox 和 Redis Stream。

### 微信小程序与支付上线申请

本清单按中国大陆运营主体、单一充电运营方收款、普通商户直连整理。平台规则核对日期为 2026-11-28。若小程序、实际服务方和收款方主体不同，先确认合同、交易展示、结算及平台授权关系，再提交进件。

| 顺序 | 办理事项 | 材料及完成标志 |
| --- | --- | --- |
| 1 | 在微信公众平台注册并完成微信认证 | 准备营业执照、法定代表人和管理员信息、管理员实名微信、注册邮箱，以及页面要求的授权材料。填写名称、头像、简介，保存正式小程序 AppID。微信认证费用以后台为准。[微信小程序接入指南](https://developers.weixin.qq.com/miniprogram/introduction) |
| 2 | 选服务类目，核实上线功能 | 境内主体按实际业务在后台核对「交通服务 → 充电服务」。站点查询、普通扫码、单次充电付费和钱包余额充值按实申报。将钱包充值、余额消费和原充值单退款的完整流程提交平台核实，取得类目和所需材料的明确答复。[微信小程序开放类目](https://developers.weixin.qq.com/miniprogram/product/material/) |
| 3 | 办小程序备案，同时推进 API 域名备案 | 在小程序后台「去备案」提交营业执照、主体负责人和小程序负责人证件、联系方式、服务说明，以及实际要求的授权或补充材料。依次完成负责人核验、平台初审、工信部短信核验和通管局审核，取得小程序备案编号并按要求展示。另确认 API 域名已完成 ICP 备案。通管局收到齐全准确材料后的办理时限为 20 个工作日，另给初审和补件留时间。[微信备案操作指引](https://developers.weixin.qq.com/miniprogram/product/record/record_guidelines.html) · [工信部 APP 备案通知](https://www.miit.gov.cn/zwgk/zcwj/wjfb/tz/art/2023/art_920db564162e4312916a01bed6540ad8.html) |
| 4 | 申请普通微信支付商户，开通 JSAPI 支付 | 企业准备营业执照、法定代表人身份证、对公结算账户及开户行信息、经办联系人、正式 AppID 和经营场景截图。新商户按「小程序」场景提交进件，完成审核、账户验证和签约。已有商户在「产品中心」核对 JSAPI 权限，缺少时申请开通。费率和结算周期以实际进件及签约页面为准。[普通商户小程序接入指引](https://pay.weixin.qq.com/static/applyment_guide/applyment_detail_miniapp.shtml) · [小程序 JSAPI 权限申请](https://pay.wechatpay.cn/doc/v3/merchant/4012791895) |
| 5 | 确认商户号和 AppID 绑定，配置 API 安全 | 若后台尚未显示有效关联，由商户平台发起授权，小程序管理员在公众平台「微信支付 → 商户号管理」确认，回商户平台复核绑定结果。准备商户 API 证书序列号、对应私钥和 APIv3 密钥，确认商户验签模式符合[微信直连支付](#微信直连支付)。[AppID 绑定指引](https://pay.wechatpay.cn/doc/v3/merchant/4013287010) · [证书与密钥说明](https://pay.wechatpay.cn/doc/v3/merchant/4024350132) |
| 6 | 配置微信能力，上传体验版并提审发布 | 在公众平台配置 `request` 合法域名、用户隐私保护指引和位置接口权限，确认手机号验证资源包。配置正式构建、实机验证后提交代码审核。提审材料附站点及端口测试方式、专用测试二维码、操作路径、价格和退款页面，以及页面要求的测试账号或说明。审核通过后按后台流程发布。[网络配置](https://developers.weixin.qq.com/miniprogram/dev/framework/ability/network.html) · [用户隐私保护指引](https://developers.weixin.qq.com/miniprogram/dev/framework/user-privacy) · [提审与发布](https://developers.weixin.qq.com/miniprogram/introduction) |

钱包储值与单次充电订单支付分别确认。微信公开类目表中，境外主体「出行与交通 → 充电服务」注明暂不支持预付储值与余额充值。境内主体的对应条目未列这条注释。境内运营方若拟开放钱包，应书面核实微信审核口径，以及运营地对充值余额、使用范围、退款和预付资金的要求。若首发采用单次充电，提审前落实相应上线范围，并验证充值入口和后台新充值能力都已关闭。已有余额的退款和售后另行安排。[微信小程序开放类目](https://developers.weixin.qq.com/miniprogram/product/material/) · [商务部单用途商业预付卡管理办法](https://www.mofcom.gov.cn/zfxxgk/zc/gz/art/2021/art_82805c1937a64a4186ea15c23fc675c5.html)

附近站点使用 `wx.getLocation`，类目通过后在「开发管理 → 接口设置」开通权限，隐私指引按实填写定位、手机号等信息的用途。`getPhoneNumber` 面向已认证的非个人小程序，企业上线前应检查可用额度。体验版核对用户拒绝授权时仍可使用无需该信息的功能。充电现场和小程序醒目位置分别展示充电电价、服务费及收费标准。[位置接口](https://developers.weixin.qq.com/miniprogram/dev/api/location/wx.getLocation.html) · [手机号验证组件](https://developers.weixin.qq.com/miniprogram/dev/framework/open-ability/getPhoneNumber.html) · [交通服务运营规范](https://developers.weixin.qq.com/miniprogram/product/jiaotonglei.html) · [电动自行车充电收费通知](https://www.ndrc.gov.cn/xwdt/tzgg/202406/t20240620_1391225.html)

### 微信上线材料与资质总表

按境内企业运营主体整理。同一份证照可供不同申请复用，按各平台页面要求分别提交。资料收集时可直接按下表核对，按需项以实际经营范围、主体关系、属地要求和后台提示为准。

| 材料或资质 | 准备内容 | 用途与适用条件 |
| --- | --- | --- |
| 企业营业执照 | 有效营业执照的清晰彩色扫描件或照片，完整显示企业名称、统一社会信用代码和登记信息。已经三证合一的企业通常可使用统一证照。 | 小程序认证、备案、微信支付商户进件。未合一或页面要求时补充组织机构代码证等材料。[微信认证](https://developers.weixin.qq.com/miniprogram/product/renzheng.html) · [商户进件](https://pay.weixin.qq.com/static/applyment_guide/applyment_detail_miniapp.shtml) |
| 法定代表人及负责人证件 | 法定代表人、主体负责人及小程序负责人使用各自的有效身份证件，按页面要求准备正反面清晰影像。填报姓名、证件号码、有效期、通讯地址等信息。 | 小程序认证、备案及支付进件。负责人核验照片按备案页面现场采集。[备案材料示例](https://developers.weixin.qq.com/miniprogram/product/record/record_material.html) |
| 账号及联系人 | 注册邮箱、管理员实名微信、管理员手机、商户管理员及经办联系人信息。备案另备主体负责人、小程序负责人和应急联系人的号码，保证本人可接听或接收核验短信。 | 账号注册、扫码核身、备案核验、授权及签约。账号持有人和岗位按各后台实际要求填写。[小程序接入](https://developers.weixin.qq.com/miniprogram/introduction) · [备案指引](https://developers.weixin.qq.com/miniprogram/product/record/record_guidelines.html) |
| 授权公函、商标或主体关系材料 | 按当前门户模板准备授权书和公函。以商标或授权品牌命名时，按要求准备商标注册证明及品牌授权。小程序与商户异主体时，准备 AppID 认证主体信息、平台授权承诺及支撑实际运营和结算关系的材料。 | 按需提交。采用委托经办、品牌授权或异主体经营时提前核对。[微信认证](https://developers.weixin.qq.com/miniprogram/product/renzheng.html) · [异主体绑定](https://pay.wechatpay.cn/doc/v3/merchant/4013287010) |
| 企业对公结算账户 | 账户名称、账号、开户行及省市信息，可配合完成账户验证。 | 支付进件和资金结算。按签约页面核对结算主体、费率和周期。[商户进件](https://pay.weixin.qq.com/static/applyment_guide/applyment_detail_miniapp.shtml) |
| 小程序资料及经营说明 | 名称、头像、简介、服务内容说明、正式 AppID。准备站点查询、设备扫码、端口选择、计价、下单支付、订单和退款的实际页面截图。 | 注册认证、类目申请、备案填报、支付经营场景审核。界面、描述和实际上线范围一致。[小程序接入](https://developers.weixin.qq.com/miniprogram/introduction) · [小程序支付申请](https://pay.wechatpay.cn/doc/v3/merchant/4012791895) |
| 充电业务类目及专项材料 | 境内「交通服务 → 充电服务」公开类目未单列专项资质。根据实际站点、自营或受托经营关系和运营地要求，核实是否需准备场地使用或运营协议及主管部门要求的许可、备案或审批材料。 | 按需提交。业务涉及其他类目或前置审批时，依对应要求补材料。[服务类目](https://developers.weixin.qq.com/miniprogram/product/material/) · [备案指引](https://developers.weixin.qq.com/miniprogram/product/record/record_guidelines.html) |
| 钱包充值与预付资金材料 | 若开放余额充值，准备充值、消费和原充值单退款的页面及流程，说明使用范围、退款规则、资金归属和处理方式，留存微信审核答复及属地要求的适用材料。 | 钱包功能纳入上线范围时准备。单次订单支付、按单预付款及差额退款可单独说明。[服务类目](https://developers.weixin.qq.com/miniprogram/product/material/) · [单用途商业预付卡管理办法](https://www.mofcom.gov.cn/zfxxgk/zc/gz/art/2021/art_82805c1937a64a4186ea15c23fc675c5.html) |
| 小程序备案资料及结果 | 备案服务内容、负责人和应急联系人信息、核验材料、互联网信息服务承诺书。由平台提供真实性核验单，按实际后台要求补授权或审批文件。审核后留存小程序备案编号及展示位置。 | 小程序 APP 备案。以所在地通管局的补件要求为准。[微信备案指引](https://developers.weixin.qq.com/miniprogram/product/record/record_guidelines.html) · [工信部备案通知](https://www.miit.gov.cn/zwgk/zcwj/wjfb/tz/art/2023/art_920db564162e4312916a01bed6540ad8.html) |
| 正式域名、ICP 备案和 HTTPS | API 域名及管理权限、域名 ICP 备案信息、DNS 配置和有效 HTTPS 证书。需要新办域名备案时，按接入服务商要求提供主体、负责人及域名证明等材料。记录 `request` 合法域名、API 地址和支付回调完整地址。 | 网站域名备案、公众平台合法域名配置和生产反代。小程序备案编号和 API 域名备案信息分别留存。[小程序网络要求](https://developers.weixin.qq.com/miniprogram/dev/framework/ability/network.html) |
| 隐私、接口权限与收费展示 | 用户隐私保护指引、实际收集信息和用途说明、定位接口申请所需的场景说明、手机号快速验证额度。准备充电电价、服务费、计费规则和退款说明页面。 | 隐私配置、能力开通及提审。按实际使用的定位、手机号能力填写。[用户隐私保护](https://developers.weixin.qq.com/miniprogram/dev/framework/user-privacy) · [定位接口](https://developers.weixin.qq.com/miniprogram/dev/api/location/wx.getLocation.html) · [手机号验证](https://developers.weixin.qq.com/miniprogram/dev/framework/open-ability/getPhoneNumber.html) · [充电收费通知](https://www.ndrc.gov.cn/xwdt/tzgg/202406/t20240620_1391225.html) |
| 支付开通及绑定记录 | 直连商户号、商户签约结果、JSAPI 已开通状态及商户号与正式 AppID 的绑定记录。 | 支付接入验收。签约和权限状态从商户平台核对。[JSAPI 权限申请](https://pay.wechatpay.cn/doc/v3/merchant/4012791895) · [AppID 绑定](https://pay.wechatpay.cn/doc/v3/merchant/4013287010) |
| 部署凭据 | 小程序 AppSecret、商户 API 证书序列号及对应私钥、32 字节 APIv3 密钥、商户侧已启用的验签模式。现有实现使用平台证书验签，SDK 下载平台证书。 | 仅用于服务端部署和验签。通过受控渠道交付，工单和文档只记录配置项及核验结果。[本项目部署配置](#微信直连支付) · [证书与密钥说明](https://pay.wechatpay.cn/doc/v3/merchant/4024350132) |
| 体验版及提审测试资料 | 正式 AppID 对应的 `weapp` 构建和体验版、审核可访问的测试环境、测试站点及端口、测试二维码、操作说明，以及提交页面要求的测试账号。留存实机小额付款、差额原路退款和商户账单的验收记录。 | 提审及上线验收。测试资料要覆盖实际公开的服务路径。[提审与发布](https://developers.weixin.qq.com/miniprogram/introduction) · [上线验收](#上线验收) |

### 微信直连支付

生产默认 `PAYMENT_MODE=disabled`。微信直连需要 `PAYMENT_MODE=wechat_direct`、有效商户参数、`WECHAT_NOTIFY_URL` 以及 `WECHAT_PRIVATE_KEY_PATH=/run/secrets/wechat-merchant.pem`。为应用容器提供受控的只读挂载：

```yaml
services:
  app:
    volumes:
      - /etc/wattharbor/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
```

将部署侧挂载保存到 `compose.payment.yaml`，启动及维护时在生产 Compose 后追加此文件。商户私钥权限须允许容器 UID 10001 读取，API v3 密钥为 32 字节。正式用户端使用真实 AppID、已配置的 API 域名和微信发布环境。

| 部署项 | 核对值 |
| --- | --- |
| 登录 | `LOGIN_MODE=wechat`。`WECHAT_APPID` 使用实际发布的 AppID，并与开发者工具、支付商户绑定一致。`WECHAT_SECRET` 为该小程序的 AppSecret，仅留服务端。 |
| 商户签名及通知解密 | `WECHAT_MCH_ID` 为直连商户号，`WECHAT_CERT_SERIAL` 为商户 API 证书序列号，`WECHAT_PRIVATE_KEY_PATH` 指向对应商户私钥，`WECHAT_APIV3_KEY` 为商户平台设置的 APIv3 密钥。私钥和密钥通过部署侧受控渠道交付。 |
| 付款通知 | `WECHAT_NOTIFY_URL` 填公网 HTTPS 全路径，例如 `https://charge.example.com/api/v1/public/payments/wechat/callback`。确认反代可接收微信的 `POST`，回调处理不需要用户登录，按渠道签名验签及解密。当前退款通过原退款号查询确认，`WECHAT_REFUND_NOTIFY_URL` 为预留配置。 |
| 小程序构建 | 从 `frontend/miniprogram` 执行 `npm ci`，设置 `TARO_APP_LOGIN_MODE=wechat` 和 `TARO_APP_API_BASE=https://charge.example.com/api/v1` 后执行 `npm run build:weapp`。在微信开发者工具用正式 AppID 导入 `dist/weapp`。公众平台的 `request` 合法域名登记 `https://charge.example.com`。真机体验关闭跳过域名和证书校验的调试选项。 |

新申请商户默认使用微信支付公钥。[当前 SDK 装配](../backend/internal/payment/channel_wechat.go) 从微信支付下载平台证书，用于验签 API 响应和回调。接入前让商户管理员核对「API 安全」中的实际模式。沿用现有代码时，按微信指引确认同一商户号下的其他系统已停用公钥模式，再切换到平台证书。商户须跟踪证书到期日并安排轮换。如需保留公钥模式，须先改造并验证响应及通知验签。商户证书序列号与微信支付公钥 ID 分属不同凭据。[官方切换指引](https://pay.wechatpay.cn/doc/v3/merchant/4015419357) · [小程序下单](https://pay.wechatpay.cn/doc/v3/merchant/4012791897)

用体验版和真实商户做小额闭环。核对微信登录、扫码选端口、付款取消、付款后服务端确认、设备启动、结束计费、预付款差额原路退回、商户账单和实际到账。付款界面返回成功后仍需等待服务端确认。复核重复通知不重复入账，退款处理中按原退款号核实。钱包若纳入已确认的发布范围，另验充值到账及按原充值单退回。

### 上线验收

验证公网 HTTPS、内部 API 隔离、真实登录和手机号授权、真实商户支付及重复回调、原路退款、实机启动及未知结果、停机、断线恢复、刷卡重复行为、最终计量、权限、审计、故障告警、外部投递及备份恢复。模拟链路不能代替真实资金或实机验证。

REGULATORY_* 当前仅为预留变量，config 与 worker 未消费，设置 http 也不会启动监管投递。仓库保留可复用的发送适配器；目标平台的字段、签名、认证、响应与任务装配尚未接线。没有外部合规认证或可据此保证的 SLA、RPO、RTO。


## 数据库

| Schema | 初始化定义 |
| --- | --- |
| `central_db` | [0001_init.sql](../backend/migrations/central_db/0001_init.sql) |
| `gateway_db` | [0001_init.sql](../backend/migrations/gateway_db/0001_init.sql) |
| `worker_db` | [0001_init.sql](../backend/migrations/worker_db/0001_init.sql) |

另有各库的 Goose 版本表。表和字段 COMMENT 描述职责、单位、状态、快照及幂等用途。部分表按月分区，查询、更新及唯一性检查须使用实际联合键。表存在不表示对应功能已在主程序接线。

初始化空库需配置 `DATABASE_URL_GATEWAY`、`DATABASE_URL_CENTRAL`、`DATABASE_URL_WORKER`，然后执行：

```sh
go run ./cmd/migrate -schema all
```

MySQL init 脚本仅在空数据卷创建三库和授权，表由 Goose 初始化。项目尚未发布，初始化定义和已登记的增量按现有版本顺序执行，发布前结构调整直接修改相应定义，不新增发布前递增迁移，也不自动建表。已执行版本不会因文件修改重新应用；既有数据需要兼容迁移，不能只删除 Goose 版本表。MySQL DDL 无事务回滚。

本次设计兼容更新使用 [upgrade-design.mjs](../scripts/db/upgrade-design.mjs)，仅连接本项目开发编排。先执行无参数预检，再停止业务写入，以 `--apply` 备份至 `.tmp/db-design-backups/` 并补字段、索引与约束。工具发现有效身份冲突、非法金额或月首归一后的唯一键碰撞即停止，不删除或合并数据，也不修改 Goose 版本。DDL 中断后可重复执行补齐缺项；生产数据先在恢复副本验证，再按变更窗口执行经过审核的同等 DDL。

```sh
node scripts/db/upgrade-design.mjs
node scripts/db/upgrade-design.mjs --apply
node scripts/db/audit-design.mjs
```

仅应用 2026-10-09 审查修复结构时使用 `node scripts/db/upgrade-design.mjs --review-fixes` 预检，再执行 `node scripts/db/upgrade-design.mjs --review-fixes --apply`。该范围只补 `central_db.admin_user.mfa_secret_version/mfa_last_step`、`charge_billing_job.generation` 与 `gateway_db.sync_delivery_retry`；执行前仍备份三库，不删除数据或改变 Goose 版本。已有列默认保留原值，新消费记录为空；上线时先补结构，再部署使用新字段及退避接口的 gateway、admin／central 与 worker，避免混用接口版本。生产使用审核后的同等 DDL，而非直接调用仅面向开发编排的脚本。

投递异常可按 `sync_delivery_retry.kind/delivery_key` 查询 `state/attempts/last_reason/next_attempt_at`。`state` 取值见迁移 SQL 的 `gateway_db.sync_delivery_retry`，其中待核实那一档表示尚未确认、不代表独立人工审核页面已装配。分类只存固定值，核对原命令／事件、冻结请求和端口占用后再处理；禁止通过标记已上报／已处理掩盖未知结果。成功项退避与源记录标记同事务删除；迟到失败不会重建已完成项。gateway 在启动时及每小时清理30天前已完成或缺源的历史，每轮最多500条短事务，取消后等待任务退出再关闭资源。未完成的 `review/retry` 即使超过30天也保留；持续增加表示待处理业务异常积压，不能通过TTL删除来隐藏。清理错误记录为 `sync delivery history cleanup`，下轮继续。

结束候选使用 `device_event.idx_pending_type(event_type,processed_at,id)`；退避关联键显式使用 `ascii_bin`，避免连接字符集导致仅利用复合主键的kind前缀。旧开发库以全量兼容预检／`--apply` 补这两项索引（另一项为退避的 `idx_updated`）；`--review-fixes` 只补退避表索引，结束候选索引沿用全量入口。两条同步查询仍有跨表到期排序，EXPLAIN通过不构成规模吞吐验收。

[audit-design.mjs](../scripts/db/audit-design.mjs) 在同一只读一致性快照核对钱包余额与流水、冻结额与未结分摊、支付已退与成功退款、资金来源归属及跨月业务号。零差异返回 0、存在差异返回 2、执行故障返回 1；只输出类别和数量。导入余额缺少期初流水、已归档流水、外部渠道尚未对账，都需要进一步核实，不能据此直接改余额或判定渠道账实一致。

[maintain-partitions.mjs](../scripts/db/maintain-partitions.mjs) 默认预览未来 12 个月的分区补齐；`--through=YYYY-MM` 指定覆盖到该月，`--apply` 才执行。它在同一连接持有维护锁，按白名单将 `p_max` 重组为新月分区并保留 `p_max`，不删除历史分区。现有季度分区保持原状；DDL 可能搬移 `p_max` 中的数据，执行前备份并避开业务高峰。可由运维按月运行；当前 worker 未装配此任务，运行账号也无 DDL 权限。

```sh
node scripts/db/maintain-partitions.mjs
node scripts/db/maintain-partitions.mjs --through=2027-12 --apply
```

上述对账与分区工具默认连接本项目开发 MySQL。其它环境使用宿主机 MySQL 客户端，设置 `MYSQL_HOST/MYSQL_PORT/MYSQL_USER/MYSQL_PWD`；容器客户端可设置 `MYSQL_CLIENT_CONTAINER` 并传入 `MYSQL_PWD`。对账账号需要相关表的 SELECT；分区维护需由维护账号执行，不向业务账号授予 ALTER。

订单快照和曲线读取 `charge_process`。计费、估费和自动停机读取 `device_event` 中的心跳及结束计量。初始化 SQL 已移除独立遥测及聚合表。归档和删除期须同时核对订单曲线、计量证据、审计、未完结任务与恢复要求；付款、退款、启动、结束的回执摘要和幂等身份必须与其资金链共同保留，不能在 30 天后独立删除。当前工具不执行归档或删除。

后台订单时间过滤分为实际启动时间、未启动回退创建时间两条范围分支，保留原 `COALESCE(started_at,created_at)` 口径；时间过滤索引、用户进行中查询复合索引已补齐。列表仍保留原页码、总数及排序契约。开发库 EXPLAIN 只能核对访问计划，生产吞吐与延迟收益须以真实数据量及负载验证；连接池默认每池最多 20 个连接，worker 持有三池，配置预算须按所有进程副本汇总。

### 迁移与运行账号

`wattharbor` 是迁移与维护账号。应用使用 `wattharbor_app`，由 `DB_APP_PASSWORD` 配置。部署任务通过本地 MySQL socket 创建账号，按布局仅授予指定 schema 的 SELECT、INSERT、UPDATE 和 DELETE。生产授予 `wattharbor`，开发授予 `central_db`、`gateway_db` 和 `worker_db`。账号配置容器通过 `network_mode: none` 隔离网络，应用容器仅持有运行账号凭据。数据库 URL 中的密码需符合 URL 编码规则。

`provision-runtime` 在迁移成功后由 Compose 执行。已有单体数据卷可继续使用，切换时保留挂载并在维护窗口执行账号配置。备份恢复到其他 MySQL 实例后须重新配置相应权限。测试、种子及数据库维护工具继续使用维护连接。

充值申请的 `idx_payment_order(payment_order_id)` 是非唯一索引，供支付回调锁定所属申请，避免扫描无关申请。已执行旧版初始化的数据库须先备份该表并核对 `SHOW INDEX FROM central_db.wallet_recharge_request`；没有此索引时执行兼容增量更新，无需重建数据：

```sql
SET SESSION lock_wait_timeout = 10;
ALTER TABLE central_db.wallet_recharge_request
  ADD INDEX idx_payment_order (payment_order_id)
    COMMENT '支付回调定位并锁定所属充值申请，避免扫描无关申请',
  ALGORITHM=INPLACE, LOCK=NONE;
```

更新后核对索引为非唯一、列为 `payment_order_id`；保留已有 NULL 和重复关联，不去重或改为 UNIQUE。索引减少无关申请的扫描争用，同一钱包仍按原行锁串行入账。

明确可丢弃的开发数据可重建：

```powershell
node scripts/db/reset-dev.mjs --reset-development-data
node scripts/dev/prepare.mjs
```

该脚本核验本地项目、停止后端、先备份到 `.tmp/`，再重建三库并清空本项目两套 Redis，会清除业务、会话及事件。需要保留数据时不得执行。

## 备份与恢复

[backup.mjs](../scripts/db/backup.mjs) 和 [restore.mjs](../scripts/db/restore.mjs) 使用 Node.js 24 与 MySQL CLI。先创建输出目录。指定 `MYSQL_CLIENT_CONTAINER` 时使用容器客户端，凭据通过 `MYSQL_PWD` 传入。直连可配置 `MYSQL_HOST`、`MYSQL_PORT`、`MYSQL_USER` 和 `MYSQL_PWD`。生产数据库端口保持在 Docker 内网。

```bash
mkdir -p .tmp/backups
node scripts/db/backup.mjs --unified .tmp/backups
```

不传 `--unified` 时备份开发三库。归档包含相应的 SQL、例程、触发器、事件和 Goose 历史。三库逐库建立一致性快照，备份前协调停止业务写入和 DDL。生产单库的 [对账](../scripts/db/audit-design.mjs) 与 [分区维护](../scripts/db/maintain-partitions.mjs) 也使用 `--unified`。Redis AOF、导出文件、Caddy 状态和部署密钥需单独备份。

恢复会替换归档声明的 schema。停止应用、核对目标数据库及归档后，生产设置 `RESTORE_CONFIRM=wattharbor`，开发设置 `RESTORE_CONFIRM="gateway_db central_db worker_db"`：

```bash
RESTORE_CONFIRM=wattharbor node scripts/db/restore.mjs "$BACKUP_ARCHIVE"
```

恢复后核对 Goose 版本、业务数据和运行权限，再用相应 Compose 配置启动 `app`。生产使用 `migrate -schema unified`，三库使用 `migrate -schema all`。

## 监控与故障定位

单体提供 `/health/live`、`/health/ready` 和 `/metrics/{role}`。live 检查进程，ready 核对依赖和同步状态。HTTP 指标按已注册路由统计，不能作为 TCP 连接数或设备在线率。

每池增加 `wattharbor_db_open_connections/in_use_connections/idle_connections/max_open_connections`，以及 `wait_total/wait_seconds_total/max_idle_closed_total/max_idle_time_closed_total/max_lifetime_closed_total` 累计指标。标签仅固定 `service/schema`；等待持续增长时结合在用数、慢查询和锁等待诊断。所有新 MySQL 物理会话显式设置 UTC 和严格 SQL 模式，避免宿主机时区影响数据库时间。

| 现象 | 核对项 |
| --- | --- |
| 服务未就绪 | 对应单库或三库 URL、Goose 版本、数据库/Redis 凭证、依赖连通性与日志 |
| 管理员无法登录 | 引导仅空表执行、账号状态、MFA、Redis 会话、实时权限及限速 |
| 设备离线 | TCP 9100、设备编号/端口数、开通状态、连接会话与心跳；断线不代表端口已停机 |
| 已付款未充电 | 支付意图、端口占用、启动命令及回执；先确定结果，不能重复启动或直接退款 |
| 已停机未结算 | 结束投递、central 回执、计费任务、计量复核和冻结快照 |
| 退款未到账 | 同一退款号的渠道结果、预留额度、执行策略及双人审核，processing 不等于成功 |
| 任务或事件积压 | 数据库任务、Outbox 发布标记、持久回执及 Redis 连通性；当前没有 Webhook 消费或通用 DLQ |
| HTTPS 异常 | 域名解析、80/443、服务器时钟、Caddy 日志及持久数据卷 |

## 模拟器操作

编号为 16 位十进制数字，须与后台 DC589 设备一致。默认两端口，`-Ports` 支持 1～20，须与后台端口数一致。各实例使用独立编号、控制端口和状态文件。

TUI 支持调功率、插拔、刷卡与移卡、余额查询、设备参数、故障、温度、电压、烟雾、校时、断线重连和重启。`Tab` 切页，`m` 打开操作菜单，方向键选择，`Enter` 提交，`Esc` 返回，`q` 退出。卡持续贴住只触发一次刷卡事件，再次刷卡前需移卡。状态文件保存进行中的充电和待确认报告，支持进程重启恢复。

TUI 的启动信息、控制服务日志与设备通信日志统一显示在“日志”页；无界面模式继续写入 stdout。退出前恢复终端并等待设备循环停止，异常退出原因由命令输出到 stderr。

无界面运行及控制示例：

```powershell
go run ./cmd/simulator-dc589 -board-id 5348240514082652 -ports 2 -tui=false -control 127.0.0.1:9190 -state-file .tmp/simulator-headless.json
Invoke-RestMethod http://127.0.0.1:9190/state
Invoke-RestMethod http://127.0.0.1:9190/events -Method Post -ContentType application/json -Body '{"type":"power","port":1,"power_deciwatts":2500}'
```

控制接口只允许 loopback 监听。`power_deciwatts=2500` 表示 250 W；刷卡使用 `{"type":"card","port":1,"card":100001}`，移卡使用 `{"type":"remove-card","port":1}`。完整事件字段见 [Input](../backend/internal/gateway/simulator/dc589/behavior.go)，场景和参数见 `go run ./cmd/simulator-dc589 -h`。

充电心跳 15 秒，空闲心跳 60 秒，网关读超时为相应周期的三倍。模拟器每秒更新计量。当前使用通用五档参数，不支持特殊双口八档扩展、心跳分片合并和 OTA；完整协议规范仍保留这些内容。

## 全链路场景验收

本节是把充电、计量、计费与退款主流程做成可重复验收的操作清单。验收对象是开发栈的单体应用和宿主机 TCP 模拟器；模拟支付与模拟器结果不构成真实微信商户、实机设备或监管投递的验收，这些仍须按 [上线验收](#上线验收) 的外部条件单独执行。

### 环境准备

1. 一键启动：`node tasks.mjs dev-up` 启动开发栈（`deploy/compose.dev.yaml`），再执行 `node scripts/dev/prepare.mjs` 建立厂商、站点、两台设备与方案。单体 HTTP 8080 与设备 TCP 9100 映射到宿主机环回地址。
2. 场景断言直接落在开发库，统一取库中最新一笔订单。注意集成测试的显式高位主键会把 `central_db.charge_order` 自增抬到超出 16 位十进制 BCD 编码的范围，gateway 启动受理随之持续返回 409（业务码 2000）；这不是业务缺陷，用 `node scripts/db/reset-dev.mjs --reset-development-data` 重建开发数据即消失。
3. 模拟器经真实 TCP 9100 接入宿主机映射端口，场景用 `-scenario` 选择（`by-energy`、`by-time`、`stop-on-command`、`reject-start`、`fault`、`silent`、`reconnect`），运行中调功率、插拔、刷卡见上一节控制接口。注意 `-power` 等功率参数单位是分瓦：`1500` 是 150 W，不是 1500 W。
4. 部分场景需要把站点生效方案换成特定模式（如 s4 的 10 分金额套餐），`scenarios.mjs budget` 已自动化该步骤；其余模式切换经后台执行，应用走乐观锁，`expected_version` 须读站点当前生效版本。

### 场景矩阵

每场景结束后按下一节清单逐表断言；金额、电量、事件链任何一格不符都按缺陷处理，不以最终状态相近豁免。

| # | 场景 | 操作 | 期望持久事实 |
| --- | --- | --- | --- |
| 1 | 电量/时长套餐自然结束 | `by-energy` 或 `by-time` 充至套餐耗尽 | 计费任务 `done`；预付与实收相等无退款；回执分段合计等于设备自报电量 |
| 2 | 用户主动停机 | `stop-on-command` 后用户端停机 | 计费 `done` 加差额自动退；首段起点与 `charge_order.started_at` 逐毫秒对齐 |
| 3 | 拒绝起充 | `reject-start` | 启动回执 `success=0`；订单转 `refunding`；全额退款 `success`；不建立端口占用 |
| 4 | 预算封顶 | 小额金额套餐加高功率持续充电 | `charge_billing_cutoff` 落 `budget_exhausted`；电费加服务费精确等于套餐价；无人工复核 |
| 5 | 掉电零功率 | 拔出或 `fault` 后等待满一分钟 | 截止原因 `automatic_stop`；电量为零全额退；不把断线当零功率提前停机 |
| 6 | 刷卡全流程 | 绑卡、刷卡首购、移卡重刷续购、**不移开重复上报**、用户停机 | 扣款、累计时长、操作凭据同事务；**不移开的第二次上报不产生第二笔扣款**；停机后结算与余额退款到账 |
| 7 | 单体中断恢复 | 充电中停止应用，启动后等待设备重连并发起停机 | 中断期间订单和设备占用保留；重连后停机、回执与计费完成 |
| 8 | 断线重连 | `reconnect` 场景断网后恢复 | 恢复后证据连续，不因缺口误判零功率停机 |

**场景 6 含一票否决项**：[ADR-0001](adr/0001-card-swipe-duplicate-report.md) 记录平台有意不引入服务端去重，代价是「同一张卡不移开而被重复上报」只能由设备侧保证不发生。该条因此是上线前的阻断条件：向模拟器控制口连发两次 `{"type":"card","port":<端口>,"card":<同一卡号>}` 且中间不发 `remove-card`，**第二笔不得产生扣款**。该断言不通过即视为阻断上线，不接受「最终余额相符」这类近似豁免——重复扣款会先发生，再被别的路径掩盖。

### 逐表断言清单

```sql
-- 订单与回执主链（按业务号或订单 ID 过滤）
SELECT status, started_at, charged_kwh, charged_seconds FROM central_db.charge_order;
SELECT success, result_code FROM central_db.charge_start_receipt;
SELECT port_no FROM central_db.active_port_charge;                -- 占用与释放
SELECT reason, cutoff_at, electric_cents, service_cents FROM central_db.charge_billing_cutoff;
SELECT meter_json FROM central_db.charge_end_receipt;              -- 段数、合计 Wh、首段起点、每段功率
SELECT status, attempts FROM central_db.charge_billing_job;
SELECT result_json FROM central_db.charge_fee_receipt;             -- 费用在 Fee 序列化 JSON 内
-- 资金
SELECT total_cents, refunded_cents, status FROM central_db.payment_order;
SELECT refund_cents, status FROM central_db.refund_record;         -- 经 biz_type/biz_id 关联订单
-- 事件与发布
SELECT event FROM central_db.charge_event_log ORDER BY id;         -- payment_confirmed → start_acked → charge_ended → fee_calculated → bill_issued
```

`meter_json` 的核对要点：分段首尾相接无空洞、合计等于设备自报总电量、首段起点等于 `charge_order.started_at`、功率分档模式下每段都带可验证功率。最后一跳超过 60 秒的尾段按已接受的精度妥协处理：电量仍归属到段，功率缺失时整单进计量复核，不得沿用旧功率或从电量反推。

### 脚本化执行

开发栈就绪后，场景驱动在 `scripts/dev/e2e/scenarios.mjs`：`scenarios.mjs <budget|s3|s1|s2|s5|s7|s4|s8>` 驱动单个场景并自动完成逐表断言，`budget` 子命令创建并应用 10 分金额套餐，仅 s4 前执行一次；s7 对应用容器执行 SIGKILL 并重新拉起，测试同一订单能否在设备重连后完成。断言统一取库中最新订单，不得并行运行多个场景；模拟器仍须按各场景以 `-scenario` 手工启动。S6（刷卡全流程）暂未脚本化，按矩阵手工执行。

模拟器须按场景逐个切换，启动命令取自下表（板号是 `prepare.mjs` 建立的 1 号设备，在 `backend` 目录下执行）：

| 场景 | 模拟器启动命令 |
| --- | --- |
| budget | 无需启动模拟器，只经后台应用方案 |
| s1 | `go run ./cmd/simulator-dc589 -scenario by-time -board-id 5348240514082652` |
| s2 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s3 | `go run ./cmd/simulator-dc589 -scenario reject-start -board-id 5348240514082652` |
| s4 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s5 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s7 | `go run ./cmd/simulator-dc589 -scenario stop-on-command -board-id 5348240514082652` |
| s8 | `go run ./cmd/simulator-dc589 -scenario reconnect -board-id 5348240514082652` |

s1、s2、s4、s5、s7 共用 `stop-on-command`，差异全在驱动步骤：s2 调用户端停机，s4 起充后把功率推到 1500 W（`power_deciwatts: 15000`）尽快耗尽 10 分预算，s5 发 `unplug`，s7 请求停机前先中断并重启单体，等待设备重连。`-power` 只决定起充前的初始功率，默认 150 W 通常无需覆盖。

s5 起充后由驱动向模拟器控制口发 `unplug`（即拔枪，端口不再连接）模拟掉电，模拟器保持会话，平台才会在连续一分钟零功率后执行自动停机并落 `automatic_stop` 截止点。这里不能用 `-scenario fault`：`fault` 在起充后按 `-fault-after`（默认 5 秒）主动上报故障，设备自行结束充电并上报 `charge_ended`，而 `AutoStopper` 只处理仍处于 `charging` 的订单，于是不会出现 `automatic_stop`，表现为 s5 断言失败。矩阵场景 5 的「拔出或 `fault` 后等待满一分钟」是手工验收口径，脚本化路径固定为「`stop-on-command` 加驱动发 `unplug`」；`fault` 用于验证设备主动报故障的另一条路径。

各场景串行执行，不得并行；换场景前先停掉上一个模拟器进程，同一板号重复接入会与残留会话冲突。

### 退出

停掉模拟器（前台 Ctrl+C 或退出命令）。开发栈可保留供后续联调复用；如需回收，执行 `node tasks.mjs dev-down`（容器与数据卷保留），核对 9100、9190 和 8080 的监听符合预期。保留场景日志与 SQL 导出作为验收证据。
