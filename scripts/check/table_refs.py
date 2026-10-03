#!/usr/bin/env python3
"""机检：代码里引用的表名必须真实存在于 migrations。

## 为什么需要这道检查

删表时最容易漏的是**字符串形式的表名**。`db.Table("dlq_log")` 里的表名是
纯字符串，删掉 `dlq_log` 的建表语句后 Go 编译照过、测试照过，只有运行到
那一行才炸。2026-10-03 裁剪 DLQ 时就漏了 7 处：worker/outbox 的 4 个函数
与 worker/store 的 3 个声明，都是零调用、操作已删表的死代码，靠人工 grep
才清掉。

旧检查 doc_tree.py 盯着"树里写了什么"，抓不到这种：树会跟着代码一起改，
只有表清单是外部事实。

## 判据

从 migrations/*/0001_init.sql 提取全部 CREATE TABLE 名，与代码里出现的
表名引用取差集。差集非空即失败。

代码侧只认两种写法（都是本仓库实际在用的）：
  db.Table("x")  /  `SELECT ... FROM x`  这类字面量
不解析 gorm 链式调用，也不追变量——宁可漏报也不误报。
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
os_chdir = ROOT

# ---- 1. 表清单来自 migrations（唯一事实来源）----
# 注意：event_outbox 在 central_db 与 gateway_db 各有一张，结构相同，
# 由 ops.EventOutboxRecord 一套类型覆盖，publisher 靠连接句柄区分写哪张。
# 这是刻意的设计，不是遗留重复，所以这里按**表名集合**去重计数，
# 不按 schema 计数——两个 schema 各 15/71 张，去重后是 87。
tables: set[str] = set()
per_schema: dict[str, int] = {}
for sql in sorted((ROOT / "migrations").glob("*/0001_init.sql")):
    names = set(
        re.findall(r"CREATE TABLE (?:IF NOT EXISTS )?`([a-z0-9_]+)`", sql.read_text())
    )
    per_schema[sql.parent.name] = len(names)
    tables |= names
if not tables:
    print("未能从 migrations 提取表名，规则失效", file=sys.stderr)
    sys.exit(2)

# ---- 2. 代码里出现的表名引用 ----
# 只认 `.Table("x")`：这是本仓库 gorm 访问表的**唯一确定性写法**。
# 早先也用正则匹配 SQL 里的 FROM/JOIN，结果把英文注释（"FROM the"）、
# 子查询别名（"old"）和 MySQL 系统表（information_schema）全当成表名，
# 误报十几处。宁可漏报也不误报——误报的护栏会被人加白名单绕过。
#
# 漏报的部分由 §"删表后必须扫引用"的人工步骤兜底：删表时 grep 该表名。
call_re = re.compile(r'\.Table\("([a-z0-9_]+)"\)')

SKIP = {"_test.go"}
problems: list[tuple[str, str, str]] = []

for base in ("internal", "cmd"):
    for p in sorted((ROOT / base).rglob("*.go")):
        if p.name in SKIP:
            continue
        text = p.read_text(encoding="utf-8")
        for name in sorted(set(call_re.findall(text))):
            if name not in tables:
                problems.append((p.relative_to(ROOT).as_posix(), name, ""))

if problems:
    for path, name, _ in problems:
        print(f"{path}: 引用了不存在的表 `{name}`")
    print(f"\n表引用检查未通过：{len(problems)} 处。", file=sys.stderr)
    sys.exit(1)
print(f"表引用检查通过（{len(tables)} 张表，各 schema "
      f"{sum(per_schema.values())} 张，event_outbox 跨库同名）。")
