#!/usr/bin/env python3
"""从实际目录结构重生成 AGENTS.md 的目录树。

## 为什么要有这个

AGENTS.md 顶部的树是维护契约（`doc_tree.py` 双向核对它），但它同时也是
人手维护的。每次 use case 迁移、改名、删功能，都得同步改几百行——
2026-10-04 一次 `domain/money` 归位，为了对齐这棵树改了五轮才通过
doc_tree 的反向检查（262 → 77 → 18 → 2 → 0）。每一次都是「漏了几个
目录没枚举到」。

树由脚本生成，目录结构才是唯一事实来源。描述文字（每个包一句话职责）
保留在 DESC 里，脚本只负责路径列表。

用法：
    python3 scripts/check/gen_tree.py            # 写入 AGENTS.md
    python3 scripts/check/gen_tree.py --check    # 只比对不写入，退出码非零表示漂移
"""
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
AGENTS = ROOT / "AGENTS.md"
MARK = "```text\ncharge-pilot/"
END = "```"

# 顶层目录与它的一句话职责。顺序即树的顺序；internal/ 下每个目录都必须登记，
# 漏登记会让树少一整块而检查仍然报「一致」。
DESC: list[tuple[str, str]] = [
    ("cmd", "部署单元，每个子目录一个进程"),
    ("internal/order", "订单表模型、编号分配与读模型"),
    ("internal/settlement", "结算：计价、账单、发票与退款"),
    ("internal/wallet", "钱包：余额、充值、流水、退款预占、频次风控"),
    ("internal/pricing", "计费方案、规则、发布与计量匹配"),
    ("internal/card", "刷卡：在线卡、扣款、累计时长与退款"),
    ("internal/device", "设备接入、遥测、会话、故障"),
    ("internal/identity", "用户、会话、登录与手机号"),
    ("internal/marketing", "优惠券、活动规则与核销"),
    ("internal/payment", "支付意图、渠道、回调与启动授权"),
    ("internal/ops", "后台账号、角色、审计与事件发件箱"),
    ("internal/export", "后台批量导出"),
    ("internal/application", "跨聚合事务编排"),
    ("internal/delivery", "对外事件契约与签名投递器"),
    ("internal/gateway", "设备接入运行时"),
    ("internal/worker", "事件消费、定时与投递编排"),
    ("internal/interface", "HTTP 接口层"),
    ("internal/protocol", "设备契约层（与 domain 平行）"),
    ("internal/infrastructure", "持久化实现"),
    ("internal/persist", "与业务无关的持久化原语"),
    ("internal/domain", "纯业务规则（零框架依赖）"),
    ("internal/platform", "无业务语义的通用能力"),
]

TAIL = """│
├── migrations/central_db/0001_init.sql
├── migrations/gateway_db/0001_init.sql
├── migrations/worker_db/0001_init.sql
│
├── scripts/check/  scripts/db/  scripts/dev/
│
├── AGENTS.md  README.md  REFACTOR-PLAN.md  Makefile  go.mod
└── admin-web/  miniprogram/"""


def unregistered() -> list[str]:
    """internal/ 下一级目录中没登记在 DESC 里的。

    树只由 DESC 遍历生成。漏登记一个目录，树会安静地少一整块，
    而 --check 反而报「一致」——所以这里必须让它显式失败。
    """
    registered = {d for d, _ in DESC}
    out = []
    for child in sorted((ROOT / "internal").iterdir()):
        if child.is_dir() and not child.name.startswith("."):
            rel = child.relative_to(ROOT).as_posix()
            if rel not in registered:
                out.append(rel)
    return out


def build() -> str:
    lines = ["```text", "charge-pilot/", "│"]
    for d, desc in DESC:
        base = ROOT / d
        if not base.is_dir():
            raise SystemExit(f"DESC 里的 {d} 在仓库中不存在——新增目录必须登记职责")
        lines.append(f"├── {d}".ljust(58) + desc)
        files = sorted(
            p.relative_to(ROOT).as_posix()
            for p in base.rglob("*.go")
            if not p.name.endswith("_test.go")
        )
        for i, name in enumerate(files):
            lines.append(f"{'│   ├── ' if i < len(files) - 1 else '│   └── '}{name}")
    lines.append(TAIL)
    lines.append("```")
    return "\n".join(lines)


def main() -> int:
    missing = unregistered()
    if missing:
        print(
            "以下目录未登记在 gen_tree.py 的 DESC 中，生成的树会漏掉它们：\n  "
            + "\n  ".join(missing),
            file=sys.stderr,
        )
        return 3
    tree = build()
    text = AGENTS.read_text()
    if MARK not in text:
        print("AGENTS.md 里找不到目录树起始标记", file=sys.stderr)
        return 2
    start = text.index(MARK)
    end = text.index(END, start + 8)
    updated = text[:start] + tree + text[end + 3:]
    if "--check" in sys.argv:
        if updated == text:
            print("目录树与实际结构一致。")
            return 0
        print("目录树已漂移，跑 python3 scripts/check/gen_tree.py 重新生成。", file=sys.stderr)
        return 1
    if updated != text:
        AGENTS.write_text(updated)
        print("目录树已重生成。")
    else:
        print("目录树无变化。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
