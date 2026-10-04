# charging 检查结果

检查日期：2026-10-05。以下结论依据当时的源码核对；所列问题仍待修复。本次文档迁移没有修改业务实现，也没有重新执行业务测试。

配套文档：[架构边界与组织约束](architecture-boundaries.md)、[模块重构计划](refactor-roadmap.md)。

## charging 已完成的边界与待修问题

当前拆分方向合理，后续模块应复用其职责划分原则，不机械复制四个子包：

- `start` 拥有启动授权、待启动查询和启动回执事务；`stop` 拥有停止授权、自动停机和计费截止冻结；`finish` 拥有结束回执、端口释放和结算任务提交。三个子包没有相互 import。
- `measurement` 只转换协议事件与计量证据，没有 IO；协议实现 `dc589` 没有进入充电用例。HTTP 启动／结束处理器已经改为绑定、调用和错误映射。
- 启动失败的余额退还调用 `settlement.RefundBalanceInTx`，刷卡写入调用 card 的事务内函数；结束回执在同一事务中提交 `settlement.EnqueueChargeBilling`。没有为了目录归属把回执事务拆成多次提交。
- gateway、worker 使用共享契约和具名客户端；`AutoStopper` 只依赖所需的 Gateway 接口。URL、令牌与 HTTP 连接池由 cmd 装配。worker 待启动查询经 central，不直接读取业务表。

结构迁移已经落地，但以下运行问题仍需修复。它们是当前代码中的问题，本次检查不判定由哪次重构引入。

| 优先级 | 当前证据与触发条件 | 影响 | 修复与验收要求 |
|---|---|---|---|
| P1 | [auto_stop.go](../internal/charging/stop/auto_stop.go) 先提交截止点再请求 STOP；下一轮遇到已有截止点直接返回错误。[deadline_store.go](../internal/charging/stop/deadline_store.go) 的刷卡时长分支也这样处理 | 截止点提交后，STOP 请求网络失败且 gateway 未持久受理时，后续循环无法再次提交 STOP；已冻结的早期订单还会中断整轮扫描，阻挡后续订单处理 | 把「截止点已冻结」作为可恢复状态。读取并沿用首次冻结内容，为仍在充电的订单继续请求同一订单的 STOP；区分冻结、网关持久受理和设备结束。单笔恢复失败不能长期阻挡其它订单。用真库验证截止点提交后模拟请求失败、下一轮恢复、并发首写及原截止点不变；用派发测试验证后续订单继续执行 |
| P1 | [meter.go](../internal/charging/stop/meter.go) 的 `noPowerForMinute` 只检查最后一条新鲜度，未检查相邻心跳缺口；[charging_samples.go](../internal/gateway/control/charging_samples.go) 丢弃不含目标端口的心跳 | 旧零功率样本与重连后的新样本会被拼成连续一分钟；遗漏目标端口的心跳也无法触发用例中已有的证据中断分支，可能错误停机 | 证据契约保留目标端口缺失事实或明确连续性信息；用例按可证明的采样间隔、时间顺序和新鲜度判断连续窗口，缺失或离线不能当零功率。覆盖长间隔重连、端口缺失、分页边界、乱序／未来时间以及正常连续零功率；必须有实际 gateway 取证路径到判定的测试 |

现有 `auto_stop_test.go` 甚至把间隔 68 秒的两条零功率心跳作为成功样例；其「缺失端口」测试直接构造事件，不能证明经过网关筛选后仍保留该事实。修复时要改进断言与输入路径，不能只保持原单测绿色。

另一个维护点：`deadline_store.go` 自己映射并锁定 card 的 `card_charge`。归属收敛时应调用 card 提供的事务内会话读取／加锁函数，再调用 order 的读取／加锁函数；保持 card 会话 → order 的原锁顺序，完整冻结事务仍由 stop 执行。这是表访问接口的补齐，不需要增加服务间请求。

2026-10-05 的 charging 检查中实际运行 `make test`、`make lint` 均通过。默认 Go 缓存目录不可读，改用仓库已忽略的 `.tmp/go-cache`、`.tmp/go-tmp` 后执行成功。该次检查未运行带 integration tag 的真库测试，也未做设备联调；此前批次的真库结果不作为这次检查的验证结论。
