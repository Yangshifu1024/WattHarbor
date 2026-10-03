#!/usr/bin/env python3
"""校验 AGENTS.md 目录树里列的文件在仓库中真实存在。

## 为什么这样判定

早先的 doc_tree.py 解析缩进树并对裸文件名做"全仓按名字匹配"。两个问题：
缩进由 │ ├ ── 混排而成，层级一旦算错就会报出几百条误报（误报的检查等于
没有检查）；裸文件名按名字匹配没有判别力——`domain/charging/order.go`
曾因 `gateway/store/` 下存在同名 order 文件而一路放行，那道检查因此一直
靠放行通过而不是靠能判别。

现在 AGENTS.md 树里的文件条目一律写相对仓库根的完整路径，本检查直接
正则提取这些路径，与实际文件集合比对，不解析缩进层级，因此没有层级算错
的风险。裸文件名不再参与校验：树里不写裸文件名。

## 覆盖

- 树里写了、仓库里没有 → 报出（树描述了不存在的文件，读者按图找代码会扑空）
- 测试文件不逐个列入树。树里用 *_test.go 交代，此处核对"每个 *_test.go
  所在目录都有可测对象"，因为 265 个测试文件列出来会让树不可维护。
"""
import pathlib
import re
import sys

DOC = pathlib.Path("AGENTS.md")
ROOTS = ("internal", "cmd", "scripts", "migrations", "admin-web", "miniprogram")
# docs/ 下的 Markdown 不参与目录树核对（那是叙述性文档，不是结构清单）
EXTS = ("go", "sql", "json", "ts", "tsx", "ps1", "mjs", "sh", "yaml")

if not DOC.exists():
    print("找不到 AGENTS.md")
    sys.exit(2)
text = DOC.read_text()
marker = "```text\ncharge-pilot/"
if marker not in text:
    print("AGENTS.md 里找不到目录树（'```text' + charge-pilot/）")
    sys.exit(2)
tree = text[text.index(marker) : text.index("```", text.index(marker) + 8)]

real = {
    p.as_posix()
    for root in ROOTS
    for p in pathlib.Path(root).rglob("*")
    if p.is_file()
    # 依赖与构建产物不是源码：node_modules 里能混进 .go 文件（flatted 的
    # golang 包就是个 .go），不排除的话反向检查会把它们当成漏列的源码。
    and "node_modules" not in p.parts
    and "dist" not in p.parts
}

problems = []
checked = 0

# 树里写的是相对仓库根的完整路径（internal/... cmd/... migrations/...）。
# 抓取形如 internal/domain/charging/scheme.go 的整段路径。
path_re = re.compile(
    r"\b((?:internal|cmd|scripts|migrations|admin-web|miniprogram)/[A-Za-z0-9_.*/{}-]+"
    r"\.(?:" + "|".join(EXTS) + r"))\b"
)
for raw in sorted(set(path_re.findall(tree))):
    if any(ch in raw for ch in "*{}"):
        continue
    checked += 1
    if raw not in real:
        problems.append(f"树里写了但仓库里没有：{raw}")

# 目录条目（以 / 结尾）必须存在。贪婪匹配到路径最后一段：
# 早先的写法 `(?:/[A-Za-z0-9_.-]+)*/` 会在倒数第二段停下，
# `internal/gateway/simulator/dc589/` 只匹配到 `internal/gateway/simulator/`，
# 于是注入一个不存在的末段目录也能通过——证伪时才发现。
dir_re = re.compile(
    r"\b((?:internal|cmd|scripts|migrations)(?:/[A-Za-z0-9_.-]+)+/)(?=\s|$|[^\w/])"
)
for d in sorted(set(dir_re.findall(tree))):
    d = d.rstrip("/")
    checked += 1
    if not any(p == d or p.startswith(d + "/") for p in real):
        problems.append(f"树里的目录不存在：{d}")

# 测试文件约定：每个 *_test.go 所在目录必须有非测试 .go
for p in sorted(x for x in real if x.endswith("_test.go")):
    d = p.rpartition("/")[0]
    if not any(
        q.endswith(".go") and not q.endswith("_test.go")
        for q in real
        if q.rpartition("/")[0] == d
    ):
        problems.append(f"测试文件所在目录没有可测对象：{p}")

# 反向：每个非测试 .go 都必须在树里出现。
# 只做正向的话，树会静默落后于仓库——2026-10-03 审计时树里 95 个 .go、
# 仓库里 315 个，223 个文件（含全部 cmd/*/main.go、internal/application、
# internal/domain）从未被列出，而检查一路绿灯。读者按树找不到东西，
# 正是这套树最初要解决的问题，只解决一半等于没解决。
#
# 限定 .go：前端源码由 tsc/vite 自己保证，树里不列；migrations/*.sql 已由
# 正向检查覆盖（有就必列），但反向不查——拆表脚本会生成中间态。
listed_go = {raw for raw in path_re.findall(tree) if raw.endswith(".go")
             and not any(ch in raw for ch in "*{}")}
for p in sorted(x for x in real
                if x.endswith(".go") and not x.endswith("_test.go")):
    checked += 1
    if p not in listed_go:
        problems.append(f"仓库里有但树里没列：{p}")

for msg in problems:
    print(msg)
if problems:
    print(f"\n目录树与仓库不一致：{len(problems)} 处。")
    sys.exit(1)
print(f"目录树检查通过（核对 {checked} 条）。")
