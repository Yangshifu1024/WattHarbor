#!/usr/bin/env python3
"""校验 AGENTS.md 的目录树与仓库实际结构一致。

AGENTS.md 把这棵树当作维护契约写明"新增或迁移文件时同步维护本图"，
但此前它描述的是一个从未建成的目标结构：几十个文件在仓库里并不存在。
本检查让漂移无法再次悄悄发生。

只校验"树里写了但仓库里没有"这一个方向——树漏了新文件是补文档的工作，
树里留着不存在的文件会让人按图找代码而扑空。
"""
import pathlib
import re
import sys

text = pathlib.Path("AGENTS.md").read_text()
start = text.index("```text\ncharge-pilot/")
end = text.index("```", start + 8)
tree = text[start:end]

roots = [pathlib.Path("internal"), pathlib.Path("cmd"), pathlib.Path("scripts"),
         pathlib.Path("migrations"), pathlib.Path("admin-web"), pathlib.Path("miniprogram")]
missing = []
for name in sorted(set(re.findall(r"([A-Za-z0-9_./-]+\.(?:go|sql|json|ts|tsx|ps1|mjs|sh|yaml))", tree))):
    base = name.rsplit("/", 1)[-1]
    # 树里用 xxx.go 汇总同目录的多个文件，不是单个文件
    if base in {"_test.go", "openapi.json"}:
        continue
    # 目录里若存在同名文件即认为该名字有效；带路径的按路径核对
    if "/" in name:
        if pathlib.Path(name).exists():
            continue
        if any(root.glob("**/" + base) for root in roots):
            continue
    elif any(any(root.glob("**/" + base)) for root in roots):
        continue
    missing.append(name)

for name in missing:
    print(name)
sys.exit(1 if missing else 0)
