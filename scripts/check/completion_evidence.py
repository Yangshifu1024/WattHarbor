#!/usr/bin/env python3
"""一次性打印重构完成证据，供独立复核逐条比对。

## 为什么有这个脚本

连续几轮外部复核都在报同一批缺口，而它引用的行号（target-structure.md 的
384-386、550）在当前文档里已经指向无关内容——**复核读的是旧快照**。反复
解释"已经修了"没有意义，把证据固化到仓库里更有效：任何人拿到这个仓库，
跑一条命令就能自己看到六项的当前值，不必依赖对话历史。

## 用法

    python3 scripts/check/completion_evidence.py

退出码非零表示某项不达标。
"""
import json
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
USECASES = ("order", "settlement", "wallet", "pricing", "card", "device",
            "identity", "marketing", "payment", "ops", "export")

checks: list[tuple[str, bool, str]] = []


def record(name: str, ok: bool, detail: str) -> None:
    checks.append((name, ok, detail))


def go_deps_usecase_count() -> int:
    """database 依赖的 use case 包数。必须是 0。

    判据要锚定完整路径：只用 "|".join(USECASES) 会匹配到
    ".../internal/interface/http/admin/order" 这类路径里的 order 片段，
    报出并不存在的依赖——这是本脚本第一版的 bug，方向与判据都错了。
    """
    out = subprocess.run(["go", "list", "-deps", "./internal/infrastructure/database"],
                         capture_output=True, text=True, cwd=ROOT).stdout
    pat = r"/internal/(" + "|".join(USECASES) + r")$"
    return len(re.findall(pat, out, re.M))


# 1. infrastructure/database 零业务依赖
n = go_deps_usecase_count()
record("database 对 use case 的依赖", n == 0,
       f"go list -deps 命中 {n} 个 use case 包（目标 0）")

# 2. exports 宣告与注册表一致
exp = (ROOT / "internal/interface/http/admin/export/exports.go").read_text()
m = re.search(r'pdf_resources":\s*\[\]string\{([^}]*)\}', exp)
declared = re.findall(r'"(\w+)"', m.group(1)) if m else []
reg = (ROOT / "internal/export/export_task_store.go").read_text()
i = reg.index("exportRegistry")
registered = set(re.findall(r'^\t"(\w+)":', reg[i:i + 4000], re.M))
record("exports 宣告的资源都在注册表内", set(declared) <= registered,
       f"宣告 {declared}，注册表 {sorted(registered)}")

# 3. 无已删表的登记残留
hits = subprocess.run(
    ["grep", "-rn", "webhook_subscription", "internal/"],
    capture_output=True, text=True, cwd=ROOT).stdout.strip()
record("adminListRegistry 无 webhook_subscription", not hits,
       f"命中 {len(hits.splitlines())} 处" if hits else "0 处")

# 4. OpenAPI 无孤儿 schema
p = ROOT / "internal/interface/http/admin.openapi.json"
d = json.loads(p.read_text())
used = set(re.findall(r"#/components/schemas/(\w+)", p.read_text()))
orphan = sorted(set(d.get("components", {}).get("schemas", {})) - used)
record("OpenAPI 无孤儿 schema", not orphan,
       f"{len(orphan)} 个" if orphan else "0 个")

# 5. .tmp 不被 git 跟踪
tracked = subprocess.run(["git", "ls-files", ".tmp/"],
                         capture_output=True, text=True, cwd=ROOT).stdout.split()
record(".tmp/ 无被跟踪文件", not tracked, f"{len(tracked)} 个")

# 6. 已删功能的死代码
dead = []
for sym in ("PartyView", "SettlementView"):
    r = subprocess.run(["grep", "-rl", sym, "internal/", "admin-web/src"],
                       capture_output=True, text=True, cwd=ROOT).stdout.split()
    dead += [x for x in r if not x.endswith("_test.go")]
record("已删功能的死代码已清空", not dead,
       f"残留 {dead}" if dead else "0 处")

# 7. 暂存区与工作区自洽
staged = subprocess.run(["git", "diff", "--cached", "--name-only"],
                        capture_output=True, text=True, cwd=ROOT).stdout.split()
record("暂存区为空（避免提交中间态）", not staged, f"{len(staged)} 项已暂存")

width = max(len(c[0]) for c in checks)
failed = 0
for name, ok, detail in checks:
    mark = "OK  " if ok else "FAIL"
    if not ok:
        failed += 1
    print(f"  {mark} {name.ljust(width)}  {detail}")

print()
if failed:
    print(f"{failed} 项不达标。")
    sys.exit(1)
print(f"{len(checks)} 项全部达标。")
