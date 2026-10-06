# charging 检查结果

修复与核验日期：2026-10-05。批次 0 的两个 P1 已修复，生命周期职责及原业务契约保持一致。

配套文档：[架构边界与组织约束](architecture-boundaries.md)、[模块重构计划](refactor-roadmap.md)。

## 生命周期与数据边界

- start 拥有启动授权、待启动查询和回执事务；stop 拥有停止授权、截止冻结和恢复；finish 拥有结束回执、端口释放和结算任务提交。三个子包互不 import，measurement 无 IO。
- 启动拒绝的余额退款调用 settlement，刷卡状态调用 card；结束回执同事务提交结算任务，回执、状态、资金、日志及 Outbox 没有拆成多次提交。
- 启动与结束回执事务不再直接写订单与 Outbox：订单锁读及 paid→charging、paid→refunding、charging→completed 的推进走 order 的 LifecycleView 与三个 InTx 能力，订单事件走 order.AppendEventInTx，Outbox 走 ops.AppendEventInTx，均在原事务原位置调用。charge_start_receipt、charge_end_receipt 与 active_port_charge 的写模型已从 order 移到 charging 各阶段，order 不再导出公共回执类型，start 与 finish 仍互不引用。
- stop 调用 card.PurchasedMinutesForOrder／LockPurchaseForOrder 及 order.LockChargingState。卡到期路径保持 card 会话→order 锁顺序；所属模块提供当前读取，不引入网络调用或反向依赖。
- charging 下八个持久化文件已由 *_store.go 改为 *_repo.go；原目录不留别名或转发。迁移及新增字段、方法、接口和核心恢复逻辑补齐中文注释。

## 问题与关闭证据

| 任务 | 实际行为 | 验证 |
|---|---|---|
| C-01／P1 | [auto_stop.go](../backend/internal/charging/stop/auto_stop.go) 优先对已冻结且仍在充电的订单重试原 STOP。恢复不重新取证或依赖计价快照；单笔失败汇总后继续后单。首次 cutoff 的时刻、原因与费用经原子 no-op upsert 保留 | 真库故障注入覆盖普通时长／刷卡两分支：提交冻结后受理未知、下一轮恢复、删除计价快照仍恢复、后单继续、已结束不再请求；两连接并发首写保留原记录且清理为零 |
| C-02／P1 | [charging_samples.go](../backend/internal/gateway/control/charging_samples.go) 保留目标端口缺失事实。心跳必须严格递增，相邻间隔及末条年龄不超过 30 秒；缺失和长间隔重建窗口，以最后观测时刻而非 now 计算一分钟 | 真 gateway MySQL 事件→实际路由→具名客户端→stop 判定覆盖连续、重连、缺失、乱序及 10080 行分页边界；默认测试另覆盖未来、重复时间及静默尾段 |
| C-03 | [deadline_repo.go](../backend/internal/charging/stop/deadline_repo.go) 不再自己映射 card_charge 或 charge_order，使用所属模块的锁定能力。已有 cutoff 返回可恢复停机状态 | 刷卡到期恢复及回执 integration 回归保持原锁顺序和幂等事务 |
| 生命周期订单归属 | [lifecycle_repo.go](../backend/internal/order/lifecycle_repo.go) 提供 LifecycleView 与三个状态推进能力；[start/receipt_schema.go](../backend/internal/charging/start/receipt_schema.go)、[finish/receipt_schema.go](../backend/internal/charging/finish/receipt_schema.go) 接管回执与端口占用的写模型。order 删除 StartReceiptRecord／EndReceiptRecord／ActivePortChargeRecord，无别名或转发 | 启动成功与重放、最终 Outbox 写入后 storage-error 与取消的整体回滚（start／finish 各两例）、中央新 owner 条件释放、结束 owner 不匹配回滚，以及既有启动拒绝退款、结束重放与计量冲突回归在独立 3307 三库与 6381 Redis 上通过 |

30 秒上限基于当前充电心跳 15 秒周期，允许一个周期的抖动，定义在共享证据契约；协议周期变化须重新核对。停止受理只证明 gateway 持久命令，设备停止与订单结束由异步回执确认。

## 实际验证范围

- make fmt、make lint、make test 通过；go mod tidy -diff 无依赖差异；go build -trimpath -o bin/ ./cmd/... 通过。
- 生命周期订单归属改造后重跑：make check 通过；独立测试栈 compose.test.yaml 的 3307 三库与 6381 Redis 由 cmd/migrate 应用 migrations 后，`go test -tags=integration -count=1 ./...` 全仓 67 个包通过，无失败、无跳过；上述命名用例逐条以 -v 确认实际执行。测试未访问开发库。
- go test -tags=integration -count=1 ./... 通过，连接已有开发栈三库，不自动建表或跳过。用户授权备份后重建开发数据，使用仓库 reset-dev 脚本；备份保存在已忽略的 .tmp。
- 针对性变异已检测：移除心跳缺口重建使 reconnect 测试失败；允许 cutoff 冲突覆盖使两连接首写测试失败。变异代码已恢复。
- 生命周期订单归属改造后补做锁判别变异：移除 `LockForLifecycleInTx` 的 `FOR UPDATE` 后，`TestConcurrentEndResultAppliesOnce` 在预期断言处失败（闸门等待的加锁语句未出现），恢复原实现后 charging 全包通过。该变异证明行锁确实随能力迁移保留，且并发用例对锁的缺失有判别力。
- 故障网络为 stub；取证 HTTP 为 loopback；设备流程使用现有 DC589 模拟器。上述证据不等于实机联调或真实渠道资金验收。
