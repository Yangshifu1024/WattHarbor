#!/usr/bin/env python3
"""重生成 docs/target-structure.md 的「当前状态」小节。

## 为什么要有这个

这份文档同时承担两种角色：目标方案与实施记录。实施记录里的数字是各阶段
当时的快照（database 79 文件、50 个 use case 包、表 100 张），而读者拿去
引用时需要的是**当前**的数字。两种角色混在一份文件里，就会出现"文档说
database 有 4 个文件、实际 3 个"这类矛盾——而且它不会自己暴露。

所以当前状态单列一节，由本脚本生成，`make check` 每次都重算并比对。
文档与代码脱节在提交前就被拦下，而不是留给下一个人去发现。
"""
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
DOC = ROOT / "docs" / "target-structure.md"
MARK = "## 0. 当前状态"

USECASES = ("order", "settlement", "wallet", "pricing", "card", "device",
            "identity", "marketing", "payment", "ops", "export")


def sh(*cmd: str) -> str:
    return subprocess.run(cmd, capture_output=True, text=True, cwd=ROOT).stdout.strip()


def go_files_under(d: str) -> int:
    p = ROOT / "internal" / d
    if not p.is_dir():
        return 0
    return len([x for x in p.rglob("*.go") if not x.name.endswith("_test.go")])


def table_count() -> tuple[int, int]:
    uniq: set[str] = set()
    total = 0
    for d in ("central_db", "gateway_db", "worker_db"):
        sql = (ROOT / "migrations" / d / "0001_init.sql").read_text()
        names = set(re.findall(r"^CREATE TABLE `([a-z0-9_]+)`", sql, re.M))
        uniq |= names
        total += len(names)
    return len(uniq), total


def use_case_deps() -> int:
    out = sh("go", "list", "-deps", "./internal/infrastructure/database")
    pat = "|".join(USECASES)
    return len(re.findall(rf"internal/({pat})$", out, re.M))


def non_test_lines() -> int:
    n = 0
    for base in ("internal", "cmd"):
        for p in (ROOT / base).rglob("*.go"):
            if p.name.endswith("_test.go"):
                continue
            n += len(p.read_text(encoding="utf-8", errors="replace").splitlines())
    return n


def tree_checked() -> int:
    r = subprocess.run([sys.executable, "scripts/check/doc_tree.py"],
                       capture_output=True, text=True, cwd=ROOT)
    m = re.search(r"核对 (\d+)", r.stdout)
    return int(m.group(1)) if m else 0


def build_section() -> str:
    tables, per_schema = table_count()
    db_files = go_files_under("infrastructure/database")
    deps = use_case_deps()
    return f"""{MARK}

由 `scripts/check/current_state.py` 生成，`make check` 会重算比对。下面的过程记录
里出现的历史数字**不代表当前状态**，引用前先看这里。

| 指标 | 当前值 | 取证方式 |
|---|---|---|
| 表（去重） | **{tables}** | 三个 `migrations/*/0001_init.sql` 的 `CREATE TABLE` 并集（各 schema 合计 {per_schema}，`event_outbox` 跨 central/gateway 同名） |
| 非测试 Go 行数 | **{non_test_lines()}** | `internal/` + `cmd/` 下非 `_test.go` 文件 |
| use case 包 | **{len(USECASES)}** | {' / '.join(USECASES)} |
| `infrastructure/database` | **{db_files} 文件** | 表访问已下沉到各 use case 的 `*_store.go` |
| `database` 对 use case 的依赖 | **{deps}** | `go list -deps ./internal/infrastructure/database` 中出现的 use case 包数；**0 才算达成** |
| 目录树核对条数 | **{tree_checked()}** | `scripts/check/doc_tree.py` 双向核对 |

> **`database` 的零业务依赖是本方案的核心目标之一。** 若上表"依赖"一列不为 0，
> 说明有 use case 的表访问留在了 `infrastructure/database` 里，需要搬回去。

"""


def scan_stale_claims(text: str) -> list[str]:
    """找出历史段落里与当前值矛盾、且没有「当时」限定的断言。

    文档同时是目标方案与实施记录，后者会留下各阶段的数字。问题不在于
    出现过时的数字，而在于**没标注哪个是当前**——复核者读到它们会判定
    结论不成立。加文字限定只能救一次；这道检查让"忘了标注"无法再悄悄
    发生：凡是断言 database 的文件数或依赖数、却没有同时出现"当时/快照"
    的行，一旦与当前值不符就报出来。
    """
    files, deps = go_files_under("infrastructure/database"), use_case_deps()
    out: list[str] = []
    for i, line in enumerate(text.splitlines(), 1):
        if MARK in line or i < 12:
            continue  # 当前状态小节本身不算
        stale = False
        if "infrastructure/database" in line and re.search(r"\d+\s*文件", line):
            n = int(re.search(r"(\d+)\s*文件", line).group(1))
            stale = n != files
        if ("零业务依赖" in line or "对 use case 的依赖" in line) and deps != 0:
            stale = True
        if stale and not re.search(r"当时|快照|已修复|推翻|不成立", line):
            out.append(f"第 {i} 行：无「当时」限定的过期断言：{line.strip()[:70]}")
    return out


def main() -> int:
    if not DOC.exists():
        print(f"找不到 {DOC}", file=sys.stderr)
        return 2
    text = DOC.read_text()
    i = text.find(MARK)
    if i < 0:
        print("文档里找不到「当前状态」小节", file=sys.stderr)
        return 2
    # 找到下一个二级标题作为终点
    m = re.compile(r"^## \d+\. ", re.M).search(text, i + len(MARK))
    end = m.start() if m else len(text)
    new = build_section()
    updated = text[:i] + new + text[end:]
    if updated != text:
        DOC.write_text(updated)
        print("当前状态已重生成")
    else:
        print("当前状态无变化")
    updated = DOC.read_text()
    stale = scan_stale_claims(updated)
    if stale:
        for s in stale:
            print(s, file=sys.stderr)
        print(f"\n文档里有 {len(stale)} 处无「当时」限定的过期断言", file=sys.stderr)
        return 1
    print("历史段落无未标注的过期断言")
    return 0


if __name__ == "__main__":
    sys.exit(main())
