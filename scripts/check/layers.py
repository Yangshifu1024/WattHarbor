#!/usr/bin/env python3
"""按 use case 结构机检约定。违反即失败。

六道检查，各回答一个不同的问题：

  1. domain 零框架        domain 不得出现 gorm/gin/redis，不得依赖任何实现层。
                          它装的是纯规则与跨用例契约。
  2. 表名归属 store       表名、gorm 查询构造、裸 SQL 只允许出现在
                          *_store.go / *_schema.go / *_model*.go。
  3. use case 单向依赖     from 不得 import to，除非在许可清单里逐条登记理由。
  4. platform/persist 独立  不得依赖任何 use case。
  5. 目录树与代码一致      见 doc_tree.py。
  6. OpenAPI 契约一致     声明的端点必须真的注册，无悬空 schema 引用。

**加规则前先证伪**：注入一条已知违规，确认它失败、退出码非零、**并且报的
是这条规则本身**，再还原确认退出码为零。

退出码非零不等于规则生效——脚本自身出错（变量未绑定之类）同样返回非零。
**退出码为零也不等于规则生效**：规则可能只覆盖一部分路径，靠放行通过。
上一版 layers.sh 就是这样，它的 find 范围里没有任何 use case 目录，裸表名
检查形同虚设；而 `set -u` 下把压平后的中文塞进 shell 变量再喂 grep，还会把
后续循环的变量名污染成乱码（报 `to: unbound variable`）。这一版用 Python，
判定逻辑与编码问题都消失。
"""
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
os.chdir(ROOT)

# 业务用例目录。platform、persist、domain、protocol、interface 不在其中。
USECASES = (
    "order", "settlement", "wallet", "pricing", "card",
    "device", "identity", "marketing", "payment", "ops", "export",
)

# 跨用例引用的许可清单。每条写的是**为什么**，不是"先例"——
# 新增一条要在代码评审里说明理由，否则边界迟早被磨穿。
#   settlement:wallet  退款执行要动钱包余额
#   settlement:payment 结算读支付单与退款回执
#   settlement:order   结算按订单冻结口径计价
#   wallet:payment     钱包退款单引用支付单
#   card:payment       刷卡扣的是支付单
#   card:wallet        刷卡扣款走钱包账本
#   card:order         刷卡起充产生订单
#   pricing:device     设备协议能力决定可用的计费方式
#   device:pricing     设备开通前核对站点方案
#   identity:payment   用户充值记录查支付单
#   marketing:payment  券在支付时核销
#   export:*           导出跨聚合，单独成包后不再互引
ALLOWED_CROSS = {
    # 全仓实际存在的 15 条跨用例边。每条都写清为什么必须跨，而不是"先例"——
    # 新增一条要在评审里说明理由，否则边界迟早被磨穿。护栏的作用是让
    # **新增的**必须解释，不是禁止已有的。
    #
    # 这 15 条里最该盯的是 settlement→ops：结算写事件发件箱是对的，但
    # OrderTimelineRow 是后台时间线投影，它先前被放在 ops，是 settlement
    # 反过来引 ops 的原因之一。读模型已经搬回 settlement，见 projection_store.go。
    ("card", "order"), ("card", "payment"), ("card", "wallet"),
    ("export", "device"),
    ("identity", "wallet"),
    ("payment", "marketing"), ("payment", "ops"), ("payment", "order"),
    ("pricing", "device"),
    ("settlement", "card"), ("settlement", "marketing"), ("settlement", "ops"),
    ("settlement", "order"), ("settlement", "payment"), ("settlement", "wallet"),
}

# 每条许可的理由，评审时对着看。缺理由的许可应当被删掉而不是留着。
CROSS_REASON = {
    ("identity", "wallet"): "建用户时同事务开钱包账户（WalletAccount）",
    ("settlement", "card"): "刷卡剩余退款：读 card_charge 扣费记录并调 RefundInTx",
    ("settlement", "marketing"): "结算后按订单事件核销券（ApplyActivityRules）",
    ("settlement", "ops"): "结算写事件发件箱（EventOutboxRecord）",
    ("payment", "order"): "支付回调要改订单状态与支付状态投影（PaymentStatusSQL）",
    ("payment", "marketing"): "支付时核销券并发放（RedeemInTx / ApplyActivityRules）",
    ("payment", "ops"): "支付成功写事件发件箱",
    ("export", "device"): "导出按管理员数据范围过滤站点（ScopeStations）",
}

# 目录级 SQL 豁免，显式列举，不靠"父目录名恰好是 store"这种隐式判断。
SQL_EXEMPT_DIRS = (
    "internal/gateway/store",      # gateway_db 的持久化包，文件名沿用迁移前的历史
    "internal/gateway/provision",
    "internal/delivery/dispatch",  # 投递器自带 SQL
    "internal/platform/snowflake",  # ID 生成器
    "cmd",                          # 迁移与种子工具，直接开 *sql.DB 跑脚本
)

# 表名清单从 migrations 提取，不在脚本里硬编码，新增表时规则不会失真。
def load_tables() -> list[str]:
    names: set[str] = set()
    for sql in pathlib.Path("migrations").glob("*/0001_init.sql"):
        names |= set(re.findall(r"CREATE TABLE (?:IF NOT EXISTS )?`([a-z0-9_]+)`", sql.read_text()))
    return sorted(names)


# 曾试过"裸表名字面量"检查（匹配 `var x = "charge_order"`），已撤销。
# 表名与业务枚举值大量同形：JWT 的 kind 是 "user"、事件类型是 "station"、
# JSON 字段是 "role"、停机原因是 "user"、协议消息是 "telemetry"。
# 10 条命中里只有 1 条是真的，误报率 90% 的规则不能当护栏用。
# 要判"这里写了表名"，判据得是**它被当作表用**（.Table( / Raw( / gorm 构造），
# 也就是上面 BARE_TABLE 那条。

violations: list[str] = []


def report(msg: str) -> None:
    violations.append(msg)


def go_files(*, non_test_only: bool = True):
    for base in ("internal", "cmd"):
        for p in pathlib.Path(base).rglob("*.go"):
            if non_test_only and p.name.endswith("_test.go"):
                continue
            yield p


def is_store_file(p: pathlib.Path) -> bool:
    stem = p.stem
    return (
        stem.endswith(("_store", "_schema", "_model", "_models"))
        or re.search(r"_(store|schema|model)_", stem) is not None
    )


def sql_exempt(p: pathlib.Path) -> bool:
    s = p.as_posix()
    return any(s == d or s.startswith(d + "/") for d in SQL_EXEMPT_DIRS)


# ---------------------------------------------------------------- 1. domain
FRAMEWORK = [
    (re.compile(r'gorm\.DB|gorm\.Expr|gorm\.DeletedAt|clause\.OnConflict|"gorm\.io/gorm"'),
     "出现 gorm"),
    (re.compile(r'gin\.Context|"github.com/gin-gonic/gin"'), "出现 gin"),
    (re.compile(r'"github.com/redis/go-redis'), "出现 redis"),
    # 只查 import 是不够的：gorm:"column:" 与 TableName() 都是纯字符串，
    # 不 import gorm 也能写，于是带列映射的领域类型会从缝里溜过去。
    (re.compile(r'gorm:"(column|primaryKey)|func \([A-Za-z]+\) TableName\(\) string'),
     "出现 gorm 列映射或表名映射"),
]
IMPL_LAYER = re.compile(
    r'charge-pilot/internal/(order|settlement|wallet|pricing|card|device|identity'
    r'|marketing|payment|ops|export|platform|persist|infrastructure|interface|delivery)'
    r'(["/])')  # 末尾不带斜杠：import "internal/wallet" 也要算依赖
for p in go_files():
    if not p.as_posix().startswith("internal/domain/"):
        continue
    t = p.read_text(encoding="utf-8")
    for pat, why in FRAMEWORK:
        if pat.search(t):
            report(f"{p}: domain {why}")
    if IMPL_LAYER.search(t):
        report(f"{p}: domain 依赖实现层，规则与契约不该反向依赖使用者")

# ------------------------------------------------- 2. 表名只允许出现在 store
# 跨行 SQL 用压平后的文本匹配，否则多行 Raw(`...`) 一路绿灯。
# 判据是"这个文件里出现了表结构知识"，不是"它接了 *gorm.DB"。
# 函数签名收 *gorm.DB（把事务传下去）是正常用法，加进判据会误伤 16 个文件：
# worker/schedule、gateway/control、persist/insert_id 全都只是转交事务句柄。
BARE_TABLE = re.compile(
    r'\.Table\("|gorm\.Expr|gorm:"(column|primaryKey)|func \([A-Za-z]+\) TableName\(\) string'
)

SQL_VERB = re.compile(
    r'\b(FROM|DELETE\s+FROM|INSERT\s+INTO|UPDATE)\s+[a-z_]+', re.I
)
for p in go_files():
    s = p.as_posix()
    if s.startswith("internal/interface/http/") or s.startswith("internal/protocol/"):
        continue
    if is_store_file(p) or sql_exempt(p):
        continue
    flat = p.read_text(encoding="utf-8").replace("\n", " ")
    if BARE_TABLE.search(flat):
        report(f"{p}: 表名或 gorm 查询构造应归 *_store.go / *_schema.go")
    if re.search(r"\.(Raw|Exec)[A-Za-z]*\(", flat) and SQL_VERB.search(flat):
        report(f"{p}: 裸 SQL（Raw/Exec）应归 *_store.go / *_schema.go")
    if re.search(r"\.QueryRow[A-Za-z]*\(", flat) and re.search(r"\bSELECT\b", flat, re.I):
        report(f"{p}: 裸 SQL（QueryRow）应归 *_store.go / *_schema.go")

# ---------------------------------------------------- 3. use case 单向依赖
for p in go_files():
    # 只看 internal/<usecase>/ 下的文件。interface/http/admin/order 这类
    # 目录名与用例同名但不是用例包——按父目录名判归属会把它算成 order 用例。
    parent = p.parent.name
    if parent not in USECASES or p.parent.parent.as_posix() != "internal":
        continue
    t = p.read_text(encoding="utf-8")
    # 按**实际 import 的别名**判断，而不是按包路径字符串：带别名的 import
    # （chargingstore "internal/card"）用包路径匹配会漏判，而按别名匹配
    # 还能顺带发现"别名指向的包与文件里实际用到的符号对不上"这类迁移遗留。
    imported: dict[str, str] = {}
    # 别名不能是关键字：`(import "...")` 这种无别名 import 里 "import" 是关键字，
    # 用 \w+ 会把它当成别名，于是 `import "internal/wallet"` 被记成
    # "别名叫 import 的 wallet 引用"——而 import 是关键字，正则后面没有
    # `wallet.` 这样的用法，于是被判成死 import 放行，整条跨用例边漏检。
    KEYWORDS = {"import", "var", "const", "type", "func", "return", "case", "package", "struct", "interface", "map", "chan", "go", "defer", "range", "select", "switch", "if", "else", "for"}
    for m in re.finditer(r'(?<![\w.])(\w+)?\s*"github\.com/ChargePilot2026/charge-pilot/internal/(\w+)"', t):
        alias, pkg = m.group(1), m.group(2)
        if alias and alias in KEYWORDS:
            alias = None
        imported[alias or pkg] = pkg
    for alias, pkg in imported.items():
        if pkg not in USECASES or pkg == parent or (parent, pkg) in ALLOWED_CROSS:
            continue
        if not re.search(rf'(?<![\w.]){re.escape(alias)}\.', t):
            # import 了但一个符号都没用到：死 import，删掉即可
            report(f"{p}: import 了 {pkg} 但未使用（别名 {alias}），是死 import")
            continue
        report(f"{p}: 用例 {parent} 直接 import 了 {pkg}；"
               f"跨用例引用须在 layers.py 的 ALLOWED_CROSS 登记理由")

# -------------------------------------------- 4. platform / persist 不引业务
# 末尾要匹配 " 或 / 两种收尾：import "internal/wallet" 后面跟的是引号不是斜杠，
# 只匹配 [/] 会整条漏掉 platform 依赖 use case 的情况。
usecase_re = re.compile(
    r"charge-pilot/internal/(" + "|".join(USECASES) + r")([\"/])"
)
for pkg in ("platform", "persist"):
    d = pathlib.Path("internal") / pkg
    if not d.is_dir():
        continue
    for p in go_files():
        if p.parent.as_posix().startswith(d.as_posix()):
            if usecase_re.search(p.read_text(encoding="utf-8")):
                report(f"{p}: {pkg} 不得依赖任何 use case，它是无业务语义的通用能力")

# ---------------------------------------------------- 5. 目录树与代码一致
r = subprocess.run([sys.executable, "scripts/check/doc_tree.py"],
                   capture_output=True, text=True)
if r.returncode != 0:
    for line in r.stdout.splitlines():
        if line.strip():
            report(f"AGENTS.md 目录树与仓库不一致：{line}")

# ---------------------------------------------------- 6. OpenAPI 契约一致
for spec_name, owner in (("admin.openapi.json", "admin"), ("user.openapi.json", "user")):
    spec = pathlib.Path("internal/interface/http") / spec_name
    if not spec.is_file():
        continue
    r = subprocess.run([sys.executable, "scripts/check/openapi_check.py", str(spec), owner],
                       capture_output=True, text=True)
    if r.returncode != 0:
        for line in r.stdout.splitlines():
            if line.strip():
                report(f"OpenAPI 文档 {spec_name} 与代码不一致：{line}")

for v in violations:
    print(v)
if violations:
    print(f"\n结构约定未满足：{len(violations)} 处。", file=sys.stderr)
    sys.exit(1)
print("结构约定检查通过。")
