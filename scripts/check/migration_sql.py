#!/usr/bin/env python3
"""检查初始化 SQL 在干净库上真的能跑通。

## 为什么要有这个

`migrations/worker_db/0001_init.sql` 里曾有一段孤儿分区子句
`/*!50100 PARTITION BY RANGE ... */` 悬在文件开头，不属于任何 `CREATE TABLE`。
它是无效 SQL，在任何 MySQL 上都报 1064。

**这个错误藏了很久**，因为开发库那个卷是重构之前就迁移过的：
goose 看到版本已是 1，重跑就是空操作，那段坏 SQL 根本没被执行。
`make check` 不跑迁移，于是它一路绿灯。

写 CI job 时用一台全新的 MySQL 试迁移，第一步就炸了——这才是它暴露的时机。

## 判据

带分区的表长这样：CREATE TABLE 的最后一行是
`) ENGINE=InnoDB ... COMMENT='...'`（**没有分号**），紧接着才是分区块，
分号落在 `*/;` 上。

所以静态判据只有一条：**分区块前面最近的非注释行若已以 `;` 结尾，
说明那张 CREATE TABLE 已经完整结束，这个分区块是悬空的。**
`worker_db` 那段孤儿的上一行正是 `SET sql_mode = '...';`。

这只是静态兜底。真正「能不能建表」只有真库迁移说了算，
所以 CI 里的 integration job 才是最终那道关。
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
# 合法形态：CREATE TABLE 的收尾行不带分号（分号在分区块末尾的 */; 上）。
# 所以「前一行已经以分号结尾」正好等价于「这张表已经建完了」——
# 这时再出现的 PARTITION 块就是孤儿。


def main() -> int:
    problems: list[str] = []
    checked = 0
    for path in sorted((ROOT / "migrations").rglob("*.sql")):
        text = path.read_text()
        lines = text.splitlines()
        # 每个片段：起点之前最近的非注释、非空行应当是 CREATE TABLE 的收尾。
        for i, line in enumerate(lines):
            if "PARTITION BY RANGE" not in line:
                continue
            checked += 1
            prev = ""
            for j in range(i - 1, -1, -1):
                candidate = lines[j].strip()
                if not candidate or candidate.startswith("--"):
                    continue
                prev = candidate
                break
            if prev.endswith(";"):
                rel = path.relative_to(ROOT).as_posix()
                problems.append(
                    f"{rel}:{i + 1}  PARTITION BY RANGE 片段前一句已结束"
                    f"（{prev[:60]!r}），该表早已建完，这里是悬空片段"
                )
    if problems:
        print("初始化 SQL 里有悬空的分区子句：", file=sys.stderr)
        for p in problems:
            print(f"  {p}", file=sys.stderr)
        print(
            "\n这类片段单独执行是无效 SQL（MySQL 1064）。"
            "开发库若是从旧版本迁移过来的，goose 会因版本号已存在而跳过重跑，"
            "于是这个错误一直不显形——只有全新库才会碰到。",
            file=sys.stderr,
        )
        return 1
    print(f"初始化 SQL 分区子句检查通过（核对 {checked} 处）。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
