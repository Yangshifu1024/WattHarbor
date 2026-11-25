# charging 检查结果

修复与核验日期：2026-10-05。批次 0 的两个 P1 已修复，生命周期职责及原业务契约保持一致。

配套文档：[架构边界与组织约束](architecture-boundaries.md)、[模块重构计划](refactor-roadmap.md)。

当前实现见 [启动回执事务](../backend/internal/charging/start/result_repo.go)、[停止截止事务](../backend/internal/charging/stop/deadline_repo.go)和[结束回执事务](../backend/internal/charging/finish/result_repo.go)。下文保留当时的关闭证据，不作为当前实现说明。

## 问题与关闭证据

| 任务 | 实际行为 | 验证 |
|---|---|---|
| C-01／P1 | [auto_stop.go](../backend/internal/charging/stop/auto_stop.go) 优先对已冻结且仍在充电的订单重试原 STOP。恢复不重新取证或依赖计价快照；单笔失败汇总后继续后单。首次 cutoff 的时刻、原因与费用经原子 no-op upsert 保留 | 真库故障注入覆盖普通时长／刷卡两分支：提交冻结后受理未知、下一轮恢复、删除计价快照仍恢复、后单继续、已结束不再请求；两连接并发首写保留原记录且清理为零 |
| C-02／P1 | [charging_samples.go](../backend/internal/gateway/control/charging_samples.go) 保留目标端口缺失事实。心跳必须严格递增，相邻间隔及末条年龄不超过 30 秒；缺失和长间隔重建窗口，以最后观测时刻而非 now 计算一分钟 | 真 gateway MySQL 事件→实际路由→具名客户端→stop 判定覆盖连续、重连、缺失、乱序及 10080 行分页边界；默认测试另覆盖未来、重复时间及静默尾段 |
| C-03 | [deadline_repo.go](../backend/internal/charging/stop/deadline_repo.go) 不再自己映射 card_charge 或 charge_order，使用所属模块的锁定能力。已有 cutoff 返回可恢复停机状态 | 刷卡到期恢复及回执 integration 回归保持原锁顺序和幂等事务 |
| 生命周期订单归属 | [lifecycle_repo.go](../backend/internal/order/lifecycle_repo.go) 提供 LifecycleView 与三个状态推进能力；[start/receipt_schema.go](../backend/internal/charging/start/receipt_schema.go)、[finish/receipt_schema.go](../backend/internal/charging/finish/receipt_schema.go) 接管回执与端口占用的写模型。order 删除 StartReceiptRecord／EndReceiptRecord／ActivePortChargeRecord，无别名或转发 | 启动成功与重放、最终 Outbox 写入后 storage-error 与取消的整体回滚（start／finish 各两例）、中央新 owner 条件释放、结束 owner 不匹配回滚，以及既有启动拒绝退款、结束重放与计量冲突回归在独立 3307 三库与 6381 Redis 上通过 |

心跳周期与取证窗口的关系见[共享取证契约](../backend/internal/contracts/gateway/charging.go)；协议周期变化须重新核对。

## 实际验证范围

- make fmt、make lint、make test 通过；go mod tidy -diff 无依赖差异；go build -trimpath -o bin/ ./cmd/... 通过。
- 生命周期订单归属改造后重跑：make check 通过；独立测试栈 compose.test.yaml 的 3307 三库与 6381 Redis 由 cmd/migrate 应用 migrations 后，`go test -tags=integration -count=1 ./...` 全仓 67 个包通过，无失败、无跳过；上述命名用例逐条以 -v 确认实际执行。测试未访问开发库。
- go test -tags=integration -count=1 ./... 通过，连接已有开发栈三库，不自动建表或跳过。用户授权备份后重建开发数据，使用仓库 reset-dev 脚本；备份保存在已忽略的 .tmp。
- 针对性变异已检测：移除心跳缺口重建使 reconnect 测试失败；允许 cutoff 冲突覆盖使两连接首写测试失败。变异代码已恢复。
- 生命周期订单归属改造后补做锁判别变异：移除 `LockForLifecycleInTx` 的 `FOR UPDATE` 后，`TestConcurrentEndResultAppliesOnce` 在预期断言处失败（闸门等待的加锁语句未出现），恢复原实现后 charging 全包通过。该变异证明行锁确实随能力迁移保留，且并发用例对锁的缺失有判别力。
- 故障网络为 stub；取证 HTTP 为 loopback；设备流程使用现有 DC589 模拟器。上述证据不等于实机联调或真实渠道资金验收。
