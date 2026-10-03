#!/usr/bin/env python3
"""layers.py 的证伪脚本：注入已知违规，确认每条规则都真的会失败。

**为什么单独一个文件**：`make check` 里"检查通过"不能证明规则有判别力。
护栏的失败模式不是报错，而是**靠放行通过**——`find` 的路径范围没覆盖到新
目录、文件名判据过严或过松、正则只匹配了一种写法，都会让检查一路绿灯。
本脚本把"它能抓到"变成可重复执行的事实。

用法：python3 scripts/check/layers_selftest.py
判据：全部用例符合预期且收尾基线为绿，才算护栏有效。
"""
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
CHECK = ROOT / "scripts" / "check" / "layers.py"

# (文件相对路径, 注入内容, 期望命中的规则片段, 期望命中?)
# 期望不命中的用例同样重要：它们防的是"规则过严，误报逼人改坏代码"。
CASES = [
    # --- 规则 1：domain 零框架 ---
    ("internal/domain/charging/selftest_probe.go",
     'package charging\n\nimport _ "gorm.io/gorm"\n', "domain 出现 gorm", True),
    ("internal/domain/charging/selftest_probe.go",
     'package charging\n\nimport _ "github.com/gin-gonic/gin"\n', "domain 出现 gin", True),
    ("internal/domain/devices/selftest_probe.go",
     'package devices\n\nimport _ "github.com/redis/go-redis/v9"\n', "domain 出现 redis", True),
    ("internal/domain/money/selftest_probe.go",
     'package money\n\nimport _ "github.com/ChargePilot2026/charge-pilot/internal/wallet"\n',
     "domain 依赖实现层", True),

    # --- 规则 2：表名只允许出现在 store ---
    ("internal/order/selftest_probe.go",
     'package order\n\nimport "gorm.io/gorm"\n\nfunc f(db *gorm.DB) { db.Table("charge_order") }\n',
     "表名或 gorm 查询构造", True),
    ("internal/settlement/selftest_probe.go",
     'package settlement\n\nimport "gorm.io/gorm"\n\nfunc f(db *gorm.DB) { db.Table("charge_bill") }\n',
     "表名或 gorm 查询构造", True),
    ("internal/wallet/selftest_probe.go",
     'package wallet\n\nimport "gorm.io/gorm"\n\nfunc f(db *gorm.DB) { db.Raw("SELECT id FROM wallet_account") }\n',
     "裸 SQL", True),
    ("internal/ops/selftest_probe.go",
     'package ops\n\ntype T struct{ A string `gorm:"column:a"` }\n', "表名或 gorm 查询构造", True),
    # 不该命中：只转交事务句柄的文件不是持久化文件
    ("internal/worker/schedule/selftest_probe.go",
     'package schedule\n\nimport "gorm.io/gorm"\n\nfunc f(db *gorm.DB) error { return nil }\n',
     "表名或 gorm", False),
    ("internal/order/selftest_probe.go",
     'package order\n\ntype X struct{ Y string }\n', "表名或 gorm", False),

    # --- 规则 3：use case 单向依赖 ---
    ("internal/pricing/selftest_probe.go",
     'package pricing\n\nimport "github.com/ChargePilot2026/charge-pilot/internal/wallet"\n\nvar _ wallet.Row\n',
     "直接 import 了 wallet", True),
    ("internal/wallet/selftest_probe.go",
     'package wallet\n\nimport "github.com/ChargePilot2026/charge-pilot/internal/payment"\n\nvar _ payment.PaymentOrderRecord\n',
     "直接 import 了 payment", True),
    # 已许可的边不该被报
    ("internal/settlement/selftest_probe.go",
     'package settlement\n\nimport "github.com/ChargePilot2026/charge-pilot/internal/wallet"\n\nvar _ wallet.Row\n',
     None, False),

    # --- 规则 4：platform / persist 不引业务 ---
    ("internal/platform/auth/selftest_probe.go",
     'package auth\n\nimport "github.com/ChargePilot2026/charge-pilot/internal/wallet"\n\nvar _ wallet.Row\n',
     "不得依赖任何 use case", True),
    ("internal/persist/selftest_probe.go",
     'package persist\n\nimport "github.com/ChargePilot2026/charge-pilot/internal/order"\n\nvar _ order.OrderView\n',
     "不得依赖任何 use case", True),
]


def run_check() -> tuple[int, str]:
    r = subprocess.run([sys.executable, str(CHECK)], capture_output=True, text=True, cwd=ROOT)
    return r.returncode, r.stdout + r.stderr


def main() -> int:
    failures = 0
    for rel, content, expect, want_hit in CASES:
        p = ROOT / rel
        p.write_text(content, encoding="utf-8")
        try:
            _, out = run_check()
        finally:
            p.unlink()
        hits = sum(1 for line in out.splitlines() if expect and expect in line)
        ok = (hits > 0) == want_hit
        if not ok:
            failures += 1
        mark = "ok  " if ok else "FAIL"
        verb = "命中" if want_hit else "不命中"
        print(f"  {mark} {(expect or '(不报)')[:40]:42} 命中={hits} 期望={verb}")

    code, out = run_check()
    if code != 0:
        failures += 1
        print(f"FAIL 收尾基线应为绿，实际 exit={code}\n{out}")
    else:
        print(f"  ok   收尾基线绿：{out.strip()}")

    if failures:
        print(f"\n护栏自检失败：{failures} 处。规则可能失效或过严。")
        return 1
    print("\n护栏自检通过：每条规则都真的会失败，且不误报。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
