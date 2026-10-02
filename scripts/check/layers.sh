#!/usr/bin/env bash
# 把几条最容易失效、也最难靠人记住的约定变成机检事实。违反即失败。
#
# 当前有四道，各自回答一个不同的问题：
#
#   1. 领域层不碰框架   —— domain 不出现 gorm / gin / redis，不依赖 protocol
#                          或任何实现层。业务规则不认识框架，才谈得上复用。
#   2. 处理器不放模型   —— internal/central 下的文件不得定义 gorm 模型或表名
#                          映射。模型归 infrastructure/database。
#   3. OpenAPI 契约一致 —— 两份文档里声明的端点必须真的注册，且没有悬空的
#                          schema 引用。只锁"文档多写"这一个方向。
#   4. 目录树与代码一致 —— AGENTS.md 的树里不能出现仓库里没有的文件。
#
# protocol 是独立的设备契约层：它不得反向依赖任何业务实现层，否则新增协议
# 就再也无法只加目录实现。这条与第 1 道同源但方向相反，单独检查。
#
# 加规则前先证伪：写一个已知违规，确认它会失败并返回非零。
# 规则不验证就等于没有——本文件曾有 53 行对已迁走目录的空转检查。

set -uo pipefail
cd "$(dirname "$0")/../.."

# 三层均有实质内容的包才分子目录；其余单层包保持一个目录，
# 避免为纯持久化或纯领域的包建立空目录。
# 领域层：子域目录，聚合由子目录名标识。
# 迁移期间 internal/central/* 仍按旧规则另行检查，此处只管已归位的部分。
domain=(
  internal/domain
)
# 表名清单从 schema 提取，不在脚本里硬编码，新增表时规则不会失真。
# sed 而非 grep -oE '[a-z_]+$'：后者在 BSD grep 上锚定行为不一致，会漏表。
tables=$(mktemp)
trap 'rm -f "$tables" "$central_tables" "$worker_tables"' EXIT
grep -ohE 'CREATE TABLE (IF NOT EXISTS )?`[a-z0-9_]+`' migrations/*/0001_init.sql |
  sed 's/.*`\(.*\)`/\1/' | sort -u >"$tables"
if [[ ! -s $tables ]]; then
  echo '未能从 migrations 提取表名，规则失效，终止' >&2
  exit 2
fi

violations=0
report() { violations=$((violations + 1)); printf '%s\n' "$1"; }

# 判定某文件相对所在目录属于哪一层。gin 优先于 gorm：混合文件按接口层处理，
# 这会把它标成违规，逼出真正的拆分而不是让它蒙混过关。
layer_of() {
  local file=$1 stem
  stem=$(basename "${file%.go}")
  if [[ $stem == *_http || $stem == http || $stem == *_middleware ]]; then
    printf 'http'
  elif [[ $stem == *_store || $stem == store || $stem == *_model || $stem == models ]]; then
    printf 'store'
  else
    printf 'domain'
  fi
}

# 迁移期间 internal/central/* 曾按"单包内文件后缀分层"检查，那批包
# （order/refund/settlement/wallet/coupon/pricing/channel/billing）已全部迁走，
# 规则随之撤销。留在表里的目录已不存在，规则空转等于没有规则。

# 领域层检查：零 gorm、零 gin、零 redis，且不得依赖协议层或实现层。
# 这是"业务规则不认识框架"这条约束的落地点。
while IFS= read -r file; do
  if grep -qE 'gorm\.DB|gorm\.Expr|gorm\.DeletedAt|clause\.OnConflict|"gorm\.io/gorm"' "$file"; then
    report "$file: 领域层出现 gorm"
  fi
  if grep -q 'gin\.Context\|"github.com/gin-gonic/gin"' "$file"; then
    report "$file: 领域层出现 gin"
  fi
  if grep -q '"github.com/redis/go-redis' "$file"; then
    report "$file: 领域层出现 redis"
  fi
  if grep -q 'charge-pilot/internal/protocol' "$file"; then
    report "$file: 领域层依赖 protocol，协议翻译应由 application 承担"
  fi
  if grep -qE 'charge-pilot/internal/(infrastructure|interface)/' "$file"; then
    report "$file: 领域层依赖实现层"
  fi
  # 结构体标签与表名映射同样算框架痕迹。只查 import 是不够的：
  # gorm:"column:" 与 TableName() 都是纯字符串，不 import gorm 也能写，
  # 于是带着列映射的领域类型会从 import 检查的缝里溜过去。
  if grep -qE 'gorm:"(column|primaryKey)|func \([A-Za-z]+\) TableName\(\) string' "$file"; then
    report "$file: 领域层出现 gorm 列映射或表名映射，持久化细节应归 infrastructure/database"
  fi
done < <(find internal/domain -name '*.go' ! -name '*_test.go')

# application 层：可依赖 domain、infrastructure 与 protocol，不得依赖 interface。
# 用例调仓储是正常编排，跨聚合事务由本层持 *gorm.DB 编排。
#
# 但"调仓储"限于调用 infrastructure 的方法。用例里一旦出现表名或查询构造，
# 取数逻辑就漏到了用例层，规则与单位会跟着库结构走。
while IFS= read -r file; do
  if grep -q 'charge-pilot/internal/interface/' "$file"; then
    report "$file: 用例层依赖接口层"
  fi
  if grep -qE '\.Table\(|gorm\.Expr|clause\.Locking|gorm:"(column|primaryKey)|func \([A-Za-z]+\) TableName\(\) string' "$file"; then
    report "$file: 用例层出现表名或 gorm 查询构造，取数应归 infrastructure/database"
  fi
done < <(find internal/application -name '*.go' ! -name '*_test.go')

# infrastructure 层：不得依赖 interface。
while IFS= read -r file; do
  if grep -q 'charge-pilot/internal/interface/' "$file"; then
    report "$file: 持久化层依赖接口层"
  fi
done < <(find internal/infrastructure -name '*.go' ! -name '*_test.go')

# protocol 契约层不得反向依赖任何业务包或设备接入运行时。
# 这条是"新协议只加 protocol/<name>/ 子目录"能成立的前提：
# 契约层一旦 import 了业务实现，实现反过来又依赖协议，加新协议就会被拖住。
#
# 覆盖 domain / application / infrastructure / interface / central / gateway /
# worker / delivery。platform 是无业务语义的通用能力，允许依赖。
#
# 原先这条只匹配了后五项，domain 与 application 漏在正则之外——它一直靠
# "没有违规"通过，而不是靠能判别。已证伪并修正。
#
# 2026-10-02 复证：internal/protocol 确实不 import 任何内部包，所以从仓库现状
# 看不出这条规则有没有判别力。逐个方向注入后确认八个全部被抓、退出码 1、
# 还原后 0：domain/charging、application、infrastructure/database、
# interface/http、central/admin、gateway/store、worker/charge、delivery。
# 现状干净不代表规则有效，这一步不能省。
while IFS= read -r file; do
  bad=$(grep -oE '"github\.com/ChargePilot2026/charge-pilot/internal/[a-z0-9_-]+(/[a-z0-9_/-]+)?"' "$file" |
        tr -d '"' | sed 's|.*/internal/||' | grep -v -e '^platform/' -e '^protocol' | sort -u | tr '\n' ' ')
  if [[ -n $bad ]]; then
    report "$file: protocol 契约层不得反向依赖业务包: $bad"
  fi
done < <(find internal/protocol -name '*.go' ! -name '*_test.go')

# OpenAPI 契约：文档里写的端点必须真的注册了，且没有悬空的 schema 引用。
# 端点下线后忘记改文档，会让调用方（包括人）对着不存在的接口排查；
# 这类漂移此前已发生——文档里长期留着 16 个已下线功能的端点。
# 只校验"文档多写"这一个方向：文档少写是补全工作，另行跟踪，不在此处阻断。
for spec in "admin.openapi.json admin" "user.openapi.json user"; do
  set -- $spec
  file="internal/interface/http/$1"
  [[ -f $file ]] || continue
  if ! problems=$(python3 scripts/check/openapi_check.py "$file" "$2"); then
    while IFS= read -r line; do
      [[ -n $line ]] && report "OpenAPI 文档 $1 与代码不一致：$line"
    done <<<"$problems"
  fi
done

# AGENTS.md 的目录树是维护契约，树里写了而仓库里没有的文件会让读文档的人扑空。
# 此前这棵树描述的是一个从未建成的目标结构，漂移长期没人发现。
if ! ghosts=$(python3 scripts/check/doc_tree.py); then
  while IFS= read -r name; do
    [[ -n $name ]] && report "AGENTS.md 目录树里的文件不存在：$name"
  done <<<"$ghosts"
fi

# worker 不得直接访问 central 拥有的业务表。进程拆分后 worker 与 central 是
# 两个进程：worker 读业务表要经 central 的内部端点，而不是拿自己的连接直连。
# worker 自己的 scheduled_task / event_outbox / dlq_* 不在其列。
#
# 表名同样从 schema 提取，新增表时规则不会失真。
central_tables=$(mktemp)
worker_tables=$(mktemp)
trap 'rm -f "$central_tables" "$worker_tables"' EXIT
grep -ohE 'CREATE TABLE (IF NOT EXISTS )?`[a-z0-9_]+`' migrations/central_db/0001_init.sql |
  sed 's/.*`\(.*\)`/\1/' | sort -u >"$central_tables"
grep -ohE 'CREATE TABLE (IF NOT EXISTS )?`[a-z0-9_]+`' migrations/worker_db/0001_init.sql |
  sed 's/.*`\(.*\)`/\1/' | sort -u >"$worker_tables"
if [[ -s $central_tables && -s $worker_tables ]]; then
  while IFS= read -r file; do
    # 只查 worker_db 的表：碰了 central 的业务表才算越界。
    bad=$(grep -ohE '\.Table\("[a-z0-9_]+"' "$file" | sed 's/.*"\(.*\)"$/\1/' |
          grep -x -F -f "$central_tables" | sort -u | tr '\n' ' ')
    if [[ -n $bad ]]; then
      report "$file: worker 直接访问 central 的业务表: $bad（应经 central 内部端点）"
    fi
  done < <(find internal/worker -name '*.go' ! -name '*_test.go')
fi

# 编排层不得出现表名或 gorm 查询构造。允许持有 schema 的只有三处：
# infrastructure/database（central_db）、gateway/store（gateway_db）、
# worker/store（worker_db）。三个进程各自拥有自己的 schema，也就各自拥有
# 自己的持久化层；把表名留在编排层只会让改表波及到不该知道库结构的地方。
#
# 豁免是显式列举的，不靠文件名匹配——文件名匹配会被新起的目录绕过去。
allowed_stores=(internal/infrastructure/database internal/gateway/store internal/worker/store internal/gateway/provision)
while IFS= read -r file; do
  skip=0
  for allowed in "${allowed_stores[@]}"; do
    [[ $file == "$allowed"/* ]] && skip=1 && break
  done
  ((skip)) && continue
  # clause.Locking 单独出现不算违规：行锁常配合模型推断的 Take(&row)，
  # 表名来自 store 定义的 TableName()，编排层并没有自己写表名。
  # 真正越界的是裸表名、gorm 列映射与查询表达式。
  if grep -qE '\.Table\("|gorm\.Expr|gorm:"(column|primaryKey)|func \([A-Za-z]+\) TableName\(\) string' "$file"; then
    report "$file: 编排层出现表名或 gorm 查询构造，应归所属 schema 的 store"
  fi
done < <(find internal/gateway internal/worker internal/interface internal/application internal/central -name '*.go' ! -name '*_test.go')

# 过渡中的 central/admin 只放 HTTP 处理：不得再定义 gorm 模型或表名映射。
# 模型留在 infrastructure/database，规则留在 domain——这条守住就不再回退。
while IFS= read -r file; do
  grep -qE 'gorm:"column:|func \([A-Za-z]+\) TableName\(\) string' "$file" &&
    report "$file: 处理器文件里出现 gorm 模型或表名映射，模型应归 infrastructure/database"
done < <(find internal/central -name '*.go' ! -name '*_test.go')

if ((violations > 0)); then
  printf '\n分层约定未满足：%d 处。修完再提交。\n' "$violations" >&2
  exit 1
fi
printf '分层约定检查通过。\n'
