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
# 真实文件清单。树里省略的根前缀不止一层："worker/" 与 "alerts/sync.go" 分占两行，
# 条目本身只写了后一段，所以按后缀匹配而不是拼根目录。
real_files = {p.as_posix() for root in roots for p in root.rglob("*") if p.is_file()}

missing = []
for name in sorted(set(re.findall(r"([A-Za-z0-9_./-]+\.(?:go|sql|json|ts|tsx|ps1|mjs|sh|yaml))", tree))):
    base = name.rsplit("/", 1)[-1]
    # 树里用 xxx.go 汇总同目录的多个文件，不是单个文件
    if base in {"_test.go", "openapi.json"}:
        continue
    if "/" in name:
        # 带路径的条目写的是确切位置，要求整段后缀真实存在。
        # 早先这里在解析不到时退回"按文件名在别处找"，于是
        # ghost_dir/main.go 会因为 cmd/user/main.go 存在而被放行：
        # 兜底越宽，规则越没有判别力。
        if any(path.endswith("/" + name) for path in real_files):
            continue
        missing.append(name)
        continue
    # 裸文件名是目录内的汇总写法，按名字在受检根下确认存在即可。
    if any(path.rsplit("/", 1)[-1] == base for path in real_files):
        continue
    missing.append(name)

for name in missing:
    print(name)
sys.exit(1 if missing else 0)
