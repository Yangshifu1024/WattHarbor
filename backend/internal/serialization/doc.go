// Package serialization 锁定 P0／P1／P2 三批枚举类型化后的**对外 JSON 形状**。
//
// 这不是「改造前 vs 改造后」的对比，而是「当前输出 vs 记录的期望」：
// 每个用例把一项枚举的**全部取值**灌进真实的对外响应结构体，用 encoding/json
// 序列化后与内联的期望字符串**逐字节**比对。任何后续改动——改 JSON tag、
// 改字段名、改类型定义、把指针改成值或值改成指针——都会让某个用例失败，
// 从而在 `git diff` 里直接看见契约变化。
//
// 铁律（改动本目录前先读）：
//  1. 只用**真实的**响应结构体（order.OrderView、money.ScanResult、wallet.RefundView、
//     card.OnlineCard、ops.AnnouncementRow 等），不在测试里另造同形结构体。
//     另造结构体测的是测试自己，不是对外契约。
//  2. 断言**精确字节**（string(got) != want），不用 JSONEq 之类的宽松比较，
//     否则测不到字段顺序、null 与省略、以及 `,string` 的差异。
//  3. 不修改期望值去迎合现状。实际输出与「应该的形状」不符时如实汇报，不在本目录外修生产代码。
//  4. 期望字符串内联在测试文件里，不单独存 golden 文件，这样 `git diff` 能直接看出契约变化。
//
// 运行：go test -tags contract ./...（不进 `node tasks.mjs check`）。
// 常见入口由 tasks.mjs 的 `test-contract` 子命令提供。
package serialization
