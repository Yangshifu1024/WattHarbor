# 仓库维护规则

## 依据与边界

- 以当前源码、路由注册、数据库初始化 SQL、Compose 配置和测试为实现依据。README 记录运行方式与业务契约；本文件记录维护规则。
- 重构的完成证据与核验方法见 [docs/REFACTOR-EVIDENCE.md](docs/REFACTOR-EVIDENCE.md)，目标结构与实施记录见 [docs/target-structure.md](docs/target-structure.md)。
- `cmd/` 只负责配置、依赖装配与进程生命周期，每个子目录是一个部署单元。**业务实现按 use case 位于 `internal/` 下**：`order`/`settlement`/`wallet`/`pricing`/`card`/`device`/`identity`/`marketing`/`payment`/`ops`/`export` 十一个用例包，纯规则与跨用例契约在 `internal/domain`，表访问在各 use case 的 `*_store.go` 与 `internal/infrastructure/database`，进程特有编排在 `internal/worker`、`internal/gateway`、`internal/delivery`，设备契约在 `internal/protocol`，通用能力在 `internal/platform`。
- 部署单元分两类。**对外**：`user`（C 端，`:8080`）、`admin`（运营后台，`:8081`）。**对内**：`central`（用例编排与内部端点，`:8082`）、`gateway`（设备接入，`:8083` 与 DC589/TCP `:9100`）、`worker`（事件消费、定时任务、外部投递，`:8085`）。对内端口不对外映射。**`central` 的 8082 在两套编排里都不映射端口**，宿主机打不开，内部调用只在容器网络内可达。开发栈另把 `gateway`:8083、`worker`:8085 绑到 `127.0.0.1` 便于本地直连排查；生产编排对这两个进程只写 `expose`，对外入口仅 Caddy。

五个进程共享同一套 use case 包，差别只在各自注册哪些路由与启动哪些后台循环——进程拆开不等于代码复制。
- 后台进程不取微信与支付凭据：它不替用户登录，也不发起支付。`config.Admin` 与 `config.Central` 分开定义，让"这个进程需要什么凭据"在类型上就是清楚的——多给一份 `WECHAT_SECRET` 就等于多一处泄露面。
- C 端充电中的遥测、历史曲线、充电采样与设备摘要，经 `GATEWAY_INTERNAL_URL` 回调 **gateway**（`:8083`）的 `/api/v1/internal/devices/*`，携服务令牌。这些端点由 `internal/gateway/control` 提供，不在 central 进程里——判断回调对象要看代码用的 base URL 字段，不能只看路径里有 `/internal`。
- 业务状态与资金写入由所属领域执行。worker 复用已定义的内部接口、任务与幂等记录，不复制计费算法或资金入账逻辑。
- 公共 HTTP 处理使用 `internal/platform/httpapi`；服务间 HTTP 调用使用 `internal/platform/serviceclient`。设备适配器实现 `protocol.Adapter`，协议解析与数据库访问分离。
- 环境变量变更须核对配置加载、进程使用、Compose 注入和模板；仅在 `.env` 中定义不能视为已生效。

## 工具与代码风格

- Go 版本以 `go.mod` 为准，当前为 Go 1.27；使用其标准库与语言能力，不为旧编译器改写代码。
- 后端检查直接使用 Makefile：`make fmt` 调用 `go fmt ./...`，`make lint` 调用 `go vet ./...`，`make test` 调用 `go test ./...`，`make layers` 调用 `scripts/check/layers.py`；另有 `make layers-selftest` 注入已知违规确认每条规则真的会失败；`make check` 顺序执行四项。
- 目录结构定稿如下。新增或迁移文件时同步维护本图，括号内注明迁移前的位置：

```text
charge-pilot/
│
├── cmd                                                   部署单元，每个子目录一个进程
│   ├── cmd/admin/main.go
│   ├── cmd/central/main.go
│   ├── cmd/devseed/main.go
│   ├── cmd/devseed/scheme.go
│   ├── cmd/gateway/main.go
│   ├── cmd/migrate/main.go
│   ├── cmd/simulator-dc589/main.go
│   ├── cmd/user/main.go
│   └── cmd/worker/main.go
├── internal/order                                        订单表模型、编号分配与读模型
│   ├── internal/order/order_number.go
│   ├── internal/order/order_package_store.go
│   ├── internal/order/order_schema.go
│   ├── internal/order/order_state_store.go
│   ├── internal/order/order_view_store.go
│   └── internal/order/pricing_snapshot_store.go
├── internal/settlement                                   结算：计价、账单、发票与退款
│   ├── internal/settlement/admin_finance_store.go
│   ├── internal/settlement/bill_store.go
│   ├── internal/settlement/billing_job_store.go
│   ├── internal/settlement/calculation_number.go
│   ├── internal/settlement/fee_calculation_store.go
│   ├── internal/settlement/invoice_store.go
│   ├── internal/settlement/manual_store.go
│   ├── internal/settlement/meter_review_store.go
│   ├── internal/settlement/order_view_store.go
│   ├── internal/settlement/refund_executor_store.go
│   ├── internal/settlement/refund_review_list_store.go
│   ├── internal/settlement/refund_review_store.go
│   └── internal/settlement/user_invoice_store.go
├── internal/wallet                                       钱包：余额、充值、流水、退款预占、频次风控
│   ├── internal/wallet/admin_wallet_risk_store.go
│   ├── internal/wallet/wallet_account_store.go
│   ├── internal/wallet/wallet_ledger_store.go
│   ├── internal/wallet/wallet_model.go
│   ├── internal/wallet/wallet_refund_store.go
│   └── internal/wallet/wallet_reservation_store.go
├── internal/pricing                                      计费方案、规则、发布与计量匹配
│   ├── internal/pricing/metering_store.go
│   ├── internal/pricing/pricing_admin_store.go
│   ├── internal/pricing/pricing_scope_store.go
│   ├── internal/pricing/pricing_store.go
│   └── internal/pricing/scheme_target_store.go
├── internal/card                                         刷卡：在线卡、扣款、累计时长与退款
│   ├── internal/card/card_admin_store.go
│   ├── internal/card/card_refund_store.go
│   ├── internal/card/card_schema.go
│   ├── internal/card/card_store.go
│   ├── internal/card/charge_store.go
│   ├── internal/card/interface_query_store.go
│   └── internal/card/user_card_store.go
├── internal/device                                       设备接入、遥测、会话、故障
│   ├── internal/device/alert_sync_store.go
│   ├── internal/device/device_admin_store.go
│   ├── internal/device/device_import.go
│   ├── internal/device/device_import_job_store.go
│   ├── internal/device/device_store.go
│   ├── internal/device/fault.go
│   ├── internal/device/identity.go
│   ├── internal/device/interface_query_store.go
│   ├── internal/device/scope.go
│   ├── internal/device/station_store.go
│   └── internal/device/user_fault_store.go
├── internal/identity                                     用户、会话、登录与手机号
│   ├── internal/identity/auth.go
│   ├── internal/identity/charge_user_store.go
│   ├── internal/identity/development.go
│   ├── internal/identity/user.go
│   ├── internal/identity/user_repository_store.go
│   └── internal/identity/wechat.go
├── internal/marketing                                    优惠券、活动规则与核销
│   ├── internal/marketing/admin_activity_store.go
│   ├── internal/marketing/admin_coupon_store.go
│   ├── internal/marketing/coupon_activity_store.go
│   ├── internal/marketing/coupon_redeem_store.go
│   ├── internal/marketing/coupon_store.go
│   ├── internal/marketing/interface_query_store.go
│   ├── internal/marketing/rules_coupon.go
│   └── internal/marketing/rules_coupon_activity.go
├── internal/payment                                      支付意图、渠道、回调与启动授权
│   ├── internal/payment/auto_stop_store.go
│   ├── internal/payment/channel_factory.go
│   ├── internal/payment/channel_refund.go
│   ├── internal/payment/channel_simulation.go
│   ├── internal/payment/channel_wechat.go
│   ├── internal/payment/errors.go
│   ├── internal/payment/interface_query_store.go
│   ├── internal/payment/money_schema.go
│   ├── internal/payment/order_authorization_store.go
│   ├── internal/payment/order_paid_start_store.go
│   ├── internal/payment/order_start_store.go
│   ├── internal/payment/payment_callback_store.go
│   ├── internal/payment/payment_checkout_store.go
│   ├── internal/payment/payment_device_store.go
│   ├── internal/payment/payment_intent_store.go
│   ├── internal/payment/payment_view_store.go
│   └── internal/payment/user_recharge_store.go
├── internal/ops                                          后台账号、角色、审计与事件发件箱
│   ├── internal/ops/admin_account_store.go
│   ├── internal/ops/admin_audit_store.go
│   ├── internal/ops/admin_ops_store.go
│   ├── internal/ops/admin_projection_store.go
│   ├── internal/ops/admin_role_store.go
│   ├── internal/ops/admin_scope_store.go
│   ├── internal/ops/admin_user_store.go
│   ├── internal/ops/event_outbox_store.go
│   └── internal/ops/interface_query_store.go
├── internal/export                                       后台批量导出
│   └── internal/export/export_task_store.go
├── internal/application                                  跨聚合事务编排
│   ├── internal/application/auto_stop.go
│   ├── internal/application/auto_stop_http.go
│   ├── internal/application/card_deadline.go
│   └── internal/application/telemetry.go
├── internal/delivery                                     对外事件契约与签名投递器
│   ├── internal/delivery/events.go
│   └── internal/delivery/senders.go
├── internal/gateway                                      设备接入运行时
│   ├── internal/gateway/control/authorize.go
│   ├── internal/gateway/control/backfill.go
│   ├── internal/gateway/control/card_delivery.go
│   ├── internal/gateway/control/cards.go
│   ├── internal/gateway/control/charge_process.go
│   ├── internal/gateway/control/charging_samples.go
│   ├── internal/gateway/control/compensate.go
│   ├── internal/gateway/control/device_faults.go
│   ├── internal/gateway/control/device_summary.go
│   ├── internal/gateway/control/historical_curve.go
│   ├── internal/gateway/control/http.go
│   ├── internal/gateway/control/scan.go
│   ├── internal/gateway/control/session.go
│   ├── internal/gateway/control/start.go
│   ├── internal/gateway/control/telemetry.go
│   ├── internal/gateway/control/user_stop.go
│   ├── internal/gateway/control/worker_sync.go
│   ├── internal/gateway/control/worker_sync_models.go
│   ├── internal/gateway/listener/server.go
│   ├── internal/gateway/provision/http.go
│   ├── internal/gateway/provision/vendors.go
│   ├── internal/gateway/session/registry.go
│   ├── internal/gateway/simulator/dc589/behavior.go
│   ├── internal/gateway/simulator/dc589/simulator.go
│   ├── internal/gateway/simulator/dc589/tui.go
│   ├── internal/gateway/store/aggregate_store.go
│   ├── internal/gateway/store/backfill_store.go
│   ├── internal/gateway/store/card_delivery_store.go
│   ├── internal/gateway/store/charge_process_store.go
│   ├── internal/gateway/store/charge_start_store.go
│   ├── internal/gateway/store/compensation_store.go
│   ├── internal/gateway/store/device_event_store.go
│   ├── internal/gateway/store/gorm_models.go
│   ├── internal/gateway/store/mysql.go
│   ├── internal/gateway/store/port_store.go
│   ├── internal/gateway/store/provision_store.go
│   ├── internal/gateway/store/scan_store.go
│   ├── internal/gateway/store/session.go
│   ├── internal/gateway/store/session_store.go
│   ├── internal/gateway/store/sync_models.go
│   ├── internal/gateway/store/telemetry_curve_store.go
│   ├── internal/gateway/store/telemetry_read_store.go
│   └── internal/gateway/store/user_stop_store.go
├── internal/worker                                       事件消费、定时与投递编排
│   ├── internal/worker/alerts/sync.go
│   ├── internal/worker/billing/dispatch.go
│   ├── internal/worker/charge/auto_stop.go
│   ├── internal/worker/charge/cards.go
│   ├── internal/worker/charge/end_sync.go
│   ├── internal/worker/charge/meter_segments.go
│   ├── internal/worker/charge/paid_start.go
│   ├── internal/worker/charge/sync.go
│   ├── internal/worker/charge/sync_client.go
│   ├── internal/worker/internaljob/http.go
│   ├── internal/worker/internaljob/ops.go
│   ├── internal/worker/outbox/publisher.go
│   ├── internal/worker/refund/dispatch.go
│   ├── internal/worker/schedule/http.go
│   ├── internal/worker/schedule/scheduler.go
│   ├── internal/worker/store/outbox_store.go
│   └── internal/worker/store/schedule_store.go
├── internal/interface                                    HTTP 接口层
│   ├── internal/interface/http/admin/admin_actor.go
│   ├── internal/interface/http/admin/alertsync/http.go
│   ├── internal/interface/http/admin/alertsync/sync.go
│   ├── internal/interface/http/admin/dashboard/dashboard.go
│   ├── internal/interface/http/admin/deps.go
│   ├── internal/interface/http/admin/device/api.go
│   ├── internal/interface/http/admin/device/device_imports.go
│   ├── internal/interface/http/admin/device/devices.go
│   ├── internal/interface/http/admin/device/meter_reviews.go
│   ├── internal/interface/http/admin/export/exports.go
│   ├── internal/interface/http/admin/export/exports_format.go
│   ├── internal/interface/http/admin/finance/api.go
│   ├── internal/interface/http/admin/finance/invoices.go
│   ├── internal/interface/http/admin/finance/refunds.go
│   ├── internal/interface/http/admin/finance/wallet_risks.go
│   ├── internal/interface/http/admin/marketing/api.go
│   ├── internal/interface/http/admin/marketing/coupon_activity.go
│   ├── internal/interface/http/admin/marketing/coupons.go
│   ├── internal/interface/http/admin/normalize.go
│   ├── internal/interface/http/admin/ops/api.go
│   ├── internal/interface/http/admin/ops/audit_log.go
│   ├── internal/interface/http/admin/ops/operations.go
│   ├── internal/interface/http/admin/order/api.go
│   ├── internal/interface/http/admin/order/casework.go
│   ├── internal/interface/http/admin/order/order_live.go
│   ├── internal/interface/http/admin/order/order_package.go
│   ├── internal/interface/http/admin/order/order_process.go
│   ├── internal/interface/http/admin/order/orders.go
│   ├── internal/interface/http/admin/order/payment_orders.go
│   ├── internal/interface/http/admin/pricing/api.go
│   ├── internal/interface/http/admin/pricing/charging_schemes.go
│   ├── internal/interface/http/admin/scope.go
│   ├── internal/interface/http/admin/scope_validate.go
│   ├── internal/interface/http/admin/service_token.go
│   ├── internal/interface/http/admin/station/api.go
│   ├── internal/interface/http/admin/station/stations.go
│   ├── internal/interface/http/admin/user/api.go
│   ├── internal/interface/http/admin/user/charge_users.go
│   ├── internal/interface/http/admin/user/online_cards.go
│   ├── internal/interface/http/admin/vendor/api.go
│   ├── internal/interface/http/admin/vendor/scope.go
│   ├── internal/interface/http/admin/vendor/vendors.go
│   ├── internal/interface/http/admin_accounts.go
│   ├── internal/interface/http/admin_accounts_store.go
│   ├── internal/interface/http/admin_auth.go
│   ├── internal/interface/http/admin_decode.go
│   ├── internal/interface/http/admin_mfa_settings.go
│   ├── internal/interface/http/admin_openapi.go
│   ├── internal/interface/http/admin_resource.go
│   ├── internal/interface/http/admin_roles.go
│   ├── internal/interface/http/auth.go
│   ├── internal/interface/http/billing_service.go
│   ├── internal/interface/http/card_api.go
│   ├── internal/interface/http/card_balance.go
│   ├── internal/interface/http/charge_card_lookup.go
│   ├── internal/interface/http/charge_feedback.go
│   ├── internal/interface/http/charge_live_meter.go
│   ├── internal/interface/http/charge_scheme_view.go
│   ├── internal/interface/http/charge_status.go
│   ├── internal/interface/http/charge_stop_authorization.go
│   ├── internal/interface/http/charge_user_account.go
│   ├── internal/interface/http/charge_user_query.go
│   ├── internal/interface/http/charge_user_stop.go
│   ├── internal/interface/http/coupon_api.go
│   ├── internal/interface/http/middleware.go
│   ├── internal/interface/http/order_authorization.go
│   ├── internal/interface/http/order_end_result.go
│   ├── internal/interface/http/order_start_result.go
│   ├── internal/interface/http/payment_development_payment.go
│   ├── internal/interface/http/payment_payment_http.go
│   ├── internal/interface/http/payment_scan.go
│   ├── internal/interface/http/phone_api.go
│   ├── internal/interface/http/refund_internal_api.go
│   ├── internal/interface/http/settlement_bill.go
│   ├── internal/interface/http/user_account_helpers.go
│   ├── internal/interface/http/user_announcement.go
│   ├── internal/interface/http/user_api.go
│   ├── internal/interface/http/user_coupon.go
│   ├── internal/interface/http/user_fault.go
│   ├── internal/interface/http/user_invoice.go
│   ├── internal/interface/http/user_station.go
│   └── internal/interface/http/user_wallet.go
├── internal/protocol                                     设备契约层（与 domain 平行）
│   ├── internal/protocol/adapter.go
│   ├── internal/protocol/command.go
│   ├── internal/protocol/dc589/adapter.go
│   ├── internal/protocol/dc589/cards.go
│   ├── internal/protocol/dc589/commands.go
│   ├── internal/protocol/dc589/config.go
│   ├── internal/protocol/dc589/device.go
│   ├── internal/protocol/dc589/fault.go
│   ├── internal/protocol/dc589/frame.go
│   ├── internal/protocol/dc589/log_data.go
│   ├── internal/protocol/dc589/measurements.go
│   ├── internal/protocol/dc589/power_control.go
│   ├── internal/protocol/dc589/session_audit.go
│   ├── internal/protocol/endpoint.go
│   ├── internal/protocol/router.go
│   └── internal/protocol/session_audit.go
├── internal/infrastructure                               持久化实现
│   ├── internal/infrastructure/database/admin_case_store.go
│   ├── internal/infrastructure/database/admin_dashboard_store.go
│   ├── internal/infrastructure/database/card_deadline_store.go
│   ├── internal/infrastructure/redis/admin_session.go
│   ├── internal/infrastructure/redis/session_store.go
│   └── internal/infrastructure/redis/session_token.go
├── internal/persist                                      与业务无关的持久化原语
│   ├── internal/persist/insert_id.go
│   └── internal/persist/page.go
├── internal/domain                                       纯业务规则（零框架依赖）
│   ├── internal/domain/charging/actual.go
│   ├── internal/domain/charging/breakdown.go
│   ├── internal/domain/charging/budget_stop.go
│   ├── internal/domain/charging/capabilities.go
│   ├── internal/domain/charging/cost.go
│   ├── internal/domain/charging/device.go
│   ├── internal/domain/charging/display.go
│   ├── internal/domain/charging/end_meter.go
│   ├── internal/domain/charging/estimate.go
│   ├── internal/domain/charging/live_meter_view.go
│   ├── internal/domain/charging/measured_segments.go
│   ├── internal/domain/charging/metering.go
│   ├── internal/domain/charging/mode.go
│   ├── internal/domain/charging/offer.go
│   ├── internal/domain/charging/offer_actual.go
│   ├── internal/domain/charging/order_package.go
│   ├── internal/domain/charging/rule.go
│   ├── internal/domain/charging/scheme.go
│   ├── internal/domain/charging/scheme_cost.go
│   ├── internal/domain/charging/segments.go
│   ├── internal/domain/charging/spec.go
│   ├── internal/domain/devices/telemetry.go
│   ├── internal/domain/money/card_number.go
│   ├── internal/domain/money/card_port.go
│   ├── internal/domain/money/review.go
│   ├── internal/domain/money/scan.go
│   ├── internal/domain/operations/account.go
│   ├── internal/domain/operations/export.go
│   ├── internal/domain/operations/scope.go
│   └── internal/domain/operations/validate.go
├── internal/platform                                     无业务语义的通用能力
│   ├── internal/platform/auth/jwt.go
│   ├── internal/platform/auth/totp.go
│   ├── internal/platform/config/config.go
│   ├── internal/platform/dbconn/mysql.go
│   ├── internal/platform/dbutil/dbutil.go
│   ├── internal/platform/httpapi/codes.go
│   ├── internal/platform/httpapi/decimal_id.go
│   ├── internal/platform/httpapi/gin.go
│   ├── internal/platform/httpapi/metrics.go
│   ├── internal/platform/netguard/guard.go
│   ├── internal/platform/phone/phone.go
│   ├── internal/platform/serviceclient/client.go
│   ├── internal/platform/serviceclient/token.go
│   └── internal/platform/snowflake/snowflake.go
│
├── migrations/central_db/0001_init.sql
├── migrations/gateway_db/0001_init.sql
├── migrations/worker_db/0001_init.sql
│
├── scripts/check/  scripts/db/  scripts/dev/
│
├── AGENTS.md  README.md  REFACTOR-PLAN.md  Makefile  go.mod
└── admin-web/  miniprogram/
```

- **代码按 use case 组织**，主轴是业务用例不是技术层。每个 use case 目录自己
  回答"这个用例怎么读写自己的数据"：

  ```
  internal/
    order/       settlement/   wallet/      pricing/    card/
    device/      identity/     marketing/   payment/    ops/
    export/                        —— 11 个 use case
    domain/                       纯规则与跨用例契约（零框架依赖）
    platform/  persist/  protocol/  delivery/  interface/  application/
    gateway/  worker/               —— 进程特有编排
    infrastructure/database/  redis/    —— 表访问
  ```

  依赖方向：

  ```
  domain       →  无（不碰 gorm/gin/redis，不依赖任何实现层）
  protocol     →  无（契约自足）
  persist      →  无（分页、自增主键等原语）
  platform     →  无（无业务语义的通用能力）
  use case     →  domain + platform + persist + 其它 use case（须登记理由）
  use case     →  infrastructure/database（表名只允许出现在 *_store.go / *_schema.go）
  ```

  真正被约束的是三条，`make layers` 全部机检：

  1. **domain 零框架**：不出现 gorm / gin / redis，不出现列映射与表名映射，
     不 import 任何实现层。
  2. **表名只归 store**：`.Table("...")`、`gorm.Expr`、`gorm:"column:"`、
     `TableName()` 与裸 SQL 只允许出现在 `*_store.go` / `*_schema.go` /
     `*_model*.go`。函数签名收 `*gorm.DB` 把事务传下去是正常用法，不算越界。
  3. **use case 之间单向**：跨用例引用必须在 `scripts/check/layers.py` 的
     `ALLOWED_CROSS` 里逐条登记理由。**没有登记的一律失败**——登记是让人
     解释耦合，不是给耦合开后门。

  `worker` 与 `gateway` 是进程特有编排，可 import use case；但 `worker`
  不得直接访问 `central` 拥有的业务表，取数据要经 central 的内部端点。
  `domain` 与 `protocol` 是平行层，互不 import。

- `internal/domain/` 装**纯规则与跨用例契约**，零 gorm、零 gin、零 Redis。
  它的判据是"有没有 IO"，不是"属不属于某个用例"——`DataScope` 被 45 处
  引用、`ChargeMode` 29 处，它们是共享契约，搬进任一 use case 都会让
  其他用例反过来引它。当前子包见上方目录树：charging / money / devices /
  operations。

- `internal/application/` 跨聚合事务编排，扁平单包。

- `internal/interface/http/` 是唯一的 HTTP 层：`user_api.go` 与 `charge_*` 系列
  服务 `/api/v1/user/*`，`admin_*` 与 `admin/` 下的 12 个业务族服务
  `/api/v1/admin/*`，`internal_*` 服务 `/api/v1/internal/*`。`gin.Context`
  只在此处出现；`middleware.go` 管鉴权，`admin/scope*.go` 管数据范围。

- `internal/protocol/` 是 gateway 与 central 共享的设备契约层，与 `domain` 平级，既不属于 `domain` 也不属于 `infrastructure`：
  - 根包只放跨进程契约：`Adapter`（`Name` + `ServeConn`）、`Sink`、`Event`、`Command`、`Registry`、`Listener`。
  - 每个协议一个子目录实现 `Adapter`，当前为 `dc589/`。帧格式、校验、指令编解码、故障码语义（`0xBB` 烟雾）、单位换算（分 → 协议余额单位）全部封闭在该目录内。
  - **协议码值、帧结构、校验算法一律不得出现在 `domain/` 与 `application/`。** 业务代码出现 `0xBB`、`dc589.` 前缀或帧相关字样即违反本约定。
  - **新增协议必须新开子目录实现 `Adapter`，不得修改 `protocol` 根包。** 若做不到，说明抽象有缺陷，应改抽象而不是把新协议逻辑塞进根包。
  - 设备领域模型对所有协议保持一致；加协议不产生新的设备类型，只产生新的语义翻译。
  - `ServeConn(context.Context, net.Conn, Sink)` 假设"一条长连接 = 一个设备会话"。若将来某协议走 HTTP 轮询，要改的是 `Adapter` 接口本身，不是各协议实现——那是接口演进，不要为想象中的协议提前过度抽象。
- 前端使用 Node.js 24、npm 和已提交的 `package-lock.json`。以 `npm ci` 安装，保留 `miniprogram/.npmrc` 的依赖配置。
- Go 注释用严肃、精炼的语言说明行为、单位、约束、并发关系或错误条件。复杂事务和协议解释保留必要细节；删除口语、重复说明、操作过程和失效历史。保留 `go:embed` 等编译指令及 `Deprecated:` 标记。
- 不提交构建产物、依赖目录、真实配置、凭证、数据库备份或临时脚本；临时文件置于 `.tmp/`。

## 数据与计算

- 金额以整数分传输和保存；费率及中间精度沿用 `internal/domain/charging/scheme.go` 的十进制计算与舍入规则。不得使用浮点金额或另建前端计费算法。
- 电量、功率和时长必须注明单位。设备 `0.1 W`、`mWh`、`Wh`、分钟和秒按协议字段转换；`1 Wh = 1000 mWh`，`1 kWh = 1000 Wh`。
- 用户 Snowflake ID 在数据库中保留 `BIGINT UNSIGNED`，公开 API、Redis 会话和前端采用十进制字符串。使用 `httpapi.DecimalID`；前端不得将大 ID 转为 JavaScript `Number`。
- 编号分配与业务记录共用事务；重试复用已持久化编号。保留历史业务编号，不重新生成或批量改写。
- 数据库连接按 UTC 解析时间；业务日界与 DC589 本地时间使用北京时间。转换必须显式指定时区，不依赖宿主机时区。
- 订单计费使用创建支付意图时冻结的完整方案、套餐及实际计量；刷卡在首次扣款事务中冻结。不得读取当前模板重算历史订单。跨费率缺少边界实测读数时进入计量复核，不按时间均摊电量或插值补造证据。
- 缺失遥测、金额、关联信息以 `null` 或明确状态表达，不用零值伪装为已测量、已结算、已到账或端口空闲。
- 手机号存入 `central_db.user.phone` 明文列，未绑定为 `NULL`，由唯一索引约束号码归属。复用 `internal/platform/phone` 的规范化、校验和展示脱敏；正式绑定须使用微信验证结果。

## 状态、幂等与审计

- 对余额、支付、退款、订单、端口归属和配额的并发更新使用既有事务与行锁顺序。同库的状态变更、幂等凭据、审计及 Outbox 应与业务写入同事务提交。
- 同一请求标识绑定同一业务内容；一致重放返回原结果，内容或身份冲突返回冲突。网络重试沿用原 `request_id`、命令 ID、支付单号和退款单号。
- 支付客户端成功、启动请求受理、STOP 回执、结束计量、费用核算和退款到账是不同事实；按持久化回执推进各自状态，不提前确认后续环节。
- 微信通知先验签、解密并核对商户、应用、订单身份、金额及渠道交易号，再入账。未知渠道结果保留查询与预留资金，不换号重提。
- 人工退款、计量分段修订和发票通过保留不同人员复核约束，第二签核对前一审核人的实时权限及原快照。人工最终金额结算沿用现有授权、实付上限、原因及审计约束；分段修订保持原总量与冻结来源，不能补造截止计量。
- 审计记录操作人、目标、请求标识及必要的前后快照；不记录密码、JWT、服务密钥、TOTP 密钥或验证码。设备日志保留关联编号和解析信息，不泄露卡号、余额、SIM 标识、面板密码或原始敏感报文。

## 权限与接口

- 后台授权实时读取有效账号、角色和权限；JWT 及前端菜单只表示会话或展示，不代替服务端授权。
- 站点、设备、厂商及配置操作沿用服务端数据范围检查。列表与详情、导出、写入和批量操作使用一致的授权条件；客户端筛选不能扩大范围。
- 用户资源以当前会话校验归属；无权访问与不存在统一按既有接口返回。服务间接口使用 `X-Service-Token`，前端不得持有服务令牌。
- 修改 API 时同步检查调用方、DTO、输入约束、错误状态和对应的 OpenAPI 文档：后台是 `internal/interface/http/admin.openapi.json`，C 端是 `user.openapi.json`（由 user 进程承载）。两份文档都要求"声明的端点必须真的注册"且"没有悬空的 schema 引用"，`make layers` 机检这两个方向。只锁"文档多写"——文档少写是补全工作，另行跟踪。文档准确性只能靠打真实接口核对：照代码"合理推断"写出来的请求体与响应形状会错，包括把整数写成字符串、给不分页的列表加分页字段。
- 维持乐观版本检查和幂等标识。前端刷新令牌不能改变业务请求身份；账号切换后的旧请求不得覆盖新会话状态。

## 设备协议

- 当前新建设备使用 DC589/TCP。新增协议在独立适配器中实现，并补齐协议帧、能力声明和模拟器验证，不在现有解析器中猜测兼容。
- 帧长度、校验、字节序、字段范围和可选负载均须严格验证。十进制 BCD 字段校验每个半字节；模块/SIM 标识保留既有非十进制半字节兼容，不按十进制统一拒绝。启动和停止回执匹配设备、端口及六字节命令会话，连接会话与命令会话不得混用。
- 设备事件先持久化，再确认上行；指令及端口占用先提交，再下发。结束结果经 central 确认后释放 gateway 端口，迟到回执不能释放新订单的占用。
- 停机条件需要可靠、连续的端口证据；离线或缺失功率不等于零功率。保持断线重连、持久重试及补偿停止的既有语义。

## 数据库变更

- 当前项目尚未发布，三个 schema 仅保留各自的 `0001_init.sql`；结构调整直接修改初始化定义，不累加发布前迁移，也不使用 GORM 自动建表。
- 每张表、字段均填写有效 MySQL `COMMENT`，注明金额和计量单位、枚举值、快照及幂等用途。维护月分区、联合键和唯一索引语义。
- 现有数据默认保留。结构不兼容时先备份并设计数据迁移；仅对明确可丢弃的开发数据使用 `scripts/db/reset-dev.ps1 -ResetDevelopmentData`。不得仅删除 Goose 版本表或无条件清空库、卷和 Redis。
- MySQL DDL 不具备事务回滚；初始化中断应针对空 schema 处理，不将失败的结构更新标为成功。

## 开发环境

- 开发环境由 `scripts/dev/start.ps1` 启动的 `compose.dev.yaml` 容器统一提供，包含后端、后台、用户端、MySQL 和 Redis。启动、数据准备、模拟器与联调顺序见 [本地运行](README.md#本地运行) 与 [模拟器](README.md#模拟器)，本节只记录使用规则。
- 可自主拉起、重启与停止开发容器，包括未运行时的 `start.ps1` 与 `docker compose -f compose.dev.yaml`。不为启动而启动：只做当次任务需要的动作，用完不留下多余容器，`start.ps1` 保留已有数据。**破坏性动作仍需确认**——重建数据卷、`down -v`、`reset-dev.ps1`、清空开发库前一律先问。
- 验证与联调优先用既有路径：`scripts/dev/start.ps1`（开发栈）、`/health/ready` 判定就绪。**不要为"多验证一点"另造旁路脚本**——自建的临时容器、临时二进制、临时灌库脚本既要自己收拾，又复现不了仓库既有的正确做法。需要新验证手段时先用仓库里已有的，或提出方案经确认后再写。
- 不得自行在宿主机启动常驻本地服务器或 dev server，也不得为 `dist` 另建静态服务；这两类一律用容器内的地址。确需一次性临时服务时避开容器占用端口，用完立即停止并说明用途。
- 验证前端渲染访问容器内的后台与用户端 H5 地址，并同时确认页面控制台无报错。
- 本地地址固定：后台 `5173/admin/`、用户 H5 `5174/`、MySQL `3306`、Redis Cache `6379` 与 Stream `6380`、DC589 `9100`；user、admin、central、gateway、worker 就绪状态查各自 HTTP 端口的 `/health/ready`。
- Go 服务没有热重载。修改后按 README 重建受影响的服务，优先只重建该服务而非整个栈；确需重启整栈时先说明理由。
- 设备联调的两个模拟器须在独立终端分别执行 `scripts/dev/simulator.ps1 -Number 1` 与 `-Number 2`，属前台交互进程，不得作为后台服务重复拉起。模拟器与模拟支付结果不得当作实机或真实资金验收。

## 验证与交付

- Git 操作一律需要用户明确授权。未获授权不得执行 `commit`、`reset`、`revert`、`push`、`rebase`、`merge`、`cherry-pick`、`tag`，不得新建或切换分支，不得改写提交历史。改动只落在工作区，由用户审阅后自行提交。`git status`、`git diff`、`git log`、`git show` 等只读命令可以直接使用。
- 交付时报告工作区的未提交改动，不代替用户提交。
- Go 业务变更运行 `make check`；涉及模块依赖时运行 `go mod tidy -diff`，涉及进程装配或构建时用 `make build SERVICE=<name>` 或 `make build-all` 构建对应 `cmd`，可执行文件一律落在已忽略的 `bin/`，不在包目录下留产物。
- 本仓库只保留单元测试，**不设需要真实数据库的集成测试**。`make check` 为秒级本地检查，可在每次改动后运行。
- 由此产生的覆盖缺口须在交付说明中写明：事务原子性、行锁并发、唯一键冲突与幂等重放没有自动化覆盖。改动资金、并发或唯一性路径时，除 `make check` 外须在开发栈上实际走一遍对应流程，并可用 `go test -race ./...` 补充竞态检测。
- 后台逻辑修改运行 `npm test` 与 `npm run build`；用户端逻辑修改运行 `npm run typecheck`、`npm test`，涉及平台适配或构建配置时构建受影响的 H5／微信目标。命令在对应前端目录执行。
- 纯文档、注释和文案修改检查链接、引用、事实与格式；不为低影响可逆修改增加重复测试。已有验证通过后，仅在新增修改、失败或未决问题需要时扩大检查。
- 测试覆盖实际契约和故障边界，重点验证并发重放、非法状态、授权范围、金额与计量不一致、外部未知结果和回执丢失；避免照抄实现的测试。
- 功能、命令、目录或部署契约改变时同步维护 README 与本文件；设备协议完整规范维护于 `protocols/dc589.md`，精确业务字段留在源码、SQL 注释和 OpenAPI，不新增重复文档树。
- 完成交付前检查差异与失效引用，报告实际修改、验证结果及仍存在的实现边界；不得把模拟器或模拟支付验证称为实机或真实资金验收。
