Status: needs-triage

# 小程序后续工作

目标：补齐仍待决策的功能，验证微信端尚未取得证据的交互。各项独立分诊，不代表已经排期。

1. [下单用券](issues/01-order-coupons.md)
2. [微信订阅消息](issues/02-wechat-subscriptions.md)
3. [首页与我的导航保活](issues/03-tab-state.md)
4. [充电状态页微信实机验收](issues/04-wechat-device-validation.md)

前端检查与双端构建见[开发与验证](../../../README.md#开发与验证)，交互规格见[小程序设计](../../design/miniprogram-ui.md)。真实设备、支付、授权等上线验收以[运行手册](../../operations.md#上线验收)为准。

2026-11-28 已核对微信公开接入资料和当前支付装配，并整理[上线申请清单](../../operations.md#微信小程序与支付上线申请)。待实际运营方确认钱包充值的提审范围、商户验签模式，并补录备案、商户审核和实机支付及退款结果。

2026-11-28 开发与生产编排引用已统一到 `deploy/`。生产检查使用 `.env.example` 通过 Compose 展开、挂载与构建文件路径检查及 Caddy 容器校验，开发 Compose 配置也已通过检查。

2026-11-28 已补[微信上线材料与资质总表](../../operations.md#微信上线材料与资质总表)，用于运营主体收集资料及核对按需补件、平台开通结果和部署凭据。
