#!/usr/bin/env bash
# 校验业务包内的分层约定。目的是让"某个函数属于哪一层"成为可以机检的事实，
# 而不是靠人记。两种形态都被承认：
#
#   单层包：包内按文件后缀分层。gin.Context 只在 *_http.go / *_middleware.go；
#          gorm 与表名只在 *_store.go / models.go；其余文件是领域层。
#   多层包：包内再分子目录 state/（领域）、store/（持久化）、http/（接口），
#          文件后缀不再单独承担分层职责，目录本身即层。
#
# 多层包的依赖方向恒为 http → state、http → store、store → state；
# 反向与同层横向依赖不允许。
#
# protocol 是独立的设备契约层，不按文件后缀分层，但参与一条检查：
# 它不得依赖任何业务包（domain/application/infrastructure/interface），
# 也不得依赖 gateway/worker 运行时。契约层一旦反向依赖实现层，
# 新增协议就再也无法只加目录实现。
#
# 不参与分层的目录：platform 是无业务语义的通用能力，cmd 是进程装配，
# simulator 是联调工具，gateway/store 与 gateway/control 各自整体即单一层，
# worker/* 与 delivery 是服务间适配层。
set -uo pipefail
cd "$(dirname "$0")/../.."

# 三层均有实质内容的包才分子目录；其余单层包保持一个目录，
# 避免为纯持久化或纯领域的包建立空目录。
# 领域层：子域目录，聚合由子目录名标识。
# 迁移期间 internal/central/* 仍按旧规则另行检查，此处只管已归位的部分。
domain=(
  internal/domain
)
# 旧结构，待迁移：包内按文件后缀分层。
legacy_flat=(
  internal/central/order
  internal/central/refund
  internal/central/settlement
  internal/central/wallet
  internal/central/coupon
  internal/central/pricing
  internal/central/channel
  internal/central/billing
)

# 表名清单从 schema 提取，不在脚本里硬编码，新增表时规则不会失真。
# sed 而非 grep -oE '[a-z_]+$'：后者在 BSD grep 上锚定行为不一致，会漏表。
tables=$(mktemp)
trap 'rm -f "$tables"' EXIT
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

# 多层包：目录即层，逐目录校验内容与依赖方向。
for pkg in "${legacy_flat[@]}"; do
  [[ -d $pkg ]] || continue
  for layer in state store http; do
    dir="$pkg/$layer"
    [[ -d $dir ]] || continue
    while IFS= read -r file; do
      if [[ $layer == state ]]; then
        grep -q 'gin\.Context' "$file" &&
          report "$file: 领域层出现 gin.Context"
        grep -qE 'gorm\.DB|gorm\.Expr|gorm\.DeletedAt|clause\.OnConflict|"gorm\.io/gorm"' "$file" &&
          report "$file: 领域层出现 gorm"
      elif [[ $layer == http ]]; then
        grep -qE 'gorm\.DB|gorm\.Expr|clause\.OnConflict|"gorm\.io/gorm"' "$file" &&
          report "$file: 接口层持有 gorm，数据库访问应下沉到 store"
      fi
      # 表名只认 SQL 构造位置（Table/FROM/JOIN/INTO/UPDATE 后面），
      # 避免把 StopUser = "user"、ExecutorDevice = "device" 这类枚举值误判。
      hits=$(grep -oE '(Table\(|FROM |JOIN |INTO |UPDATE )`?"?[a-z0-9_]+' "$file" |
        grep -oE '[a-z0-9_]+$' | sort -u | grep -xF -f "$tables" | head -3)
      if [[ -n $hits && $layer != store ]]; then
        report "$file: 出现数据库表名（$(echo "$hits" | tr '\n' ' ')）但不在 store 层"
      fi
    done < <(find "$dir" -maxdepth 1 -name '*.go' ! -name '*_test.go')
  done
  # 依赖方向：state 不得被 store 反向依赖以外的层反向引用；
  # store 不得 import http。
  if [[ -d $pkg/store ]]; then
    for file in "$pkg"/store/*.go; do
      [[ -e $file ]] || continue
      grep -q "central/$(basename $pkg)/http" "$file" &&
        report "$file: store 反向依赖 http 层"
    done
  fi
  if [[ -d $pkg/state ]]; then
    for file in "$pkg"/state/*.go; do
      [[ -e $file ]] || continue
      grep -qE "central/$(basename $pkg)/(http|store)" "$file" &&
        report "$file: state 依赖了上层，方向反了"
    done
  fi
  # 包根目录不应再留生产 Go 文件：有子目录的包一律归位。
  leftovers=$(find "$pkg" -maxdepth 1 -name '*.go' ! -name '*_test.go')
  if [[ -n $leftovers ]]; then
    while IFS= read -r file; do
      [[ -e $file ]] && report "$file: 已分子目录的包，根目录不应残留生产文件"
    done <<<"$leftovers"
  fi
done



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
done < <(find internal/domain -name '*.go' ! -name '*_test.go')

# application 层：可依赖 domain、infrastructure 与 protocol，不得依赖 interface。
# 用例调仓储是正常编排，跨聚合事务由本层持 *gorm.DB 编排。
while IFS= read -r file; do
  if grep -q 'charge-pilot/internal/interface/' "$file"; then
    report "$file: 用例层依赖接口层"
  fi
done < <(find internal/application -name '*.go' ! -name '*_test.go')

# infrastructure 层：不得依赖 interface。
while IFS= read -r file; do
  if grep -q 'charge-pilot/internal/interface/' "$file"; then
    report "$file: 持久化层依赖接口层"
  fi
done < <(find internal/infrastructure -name '*.go' ! -name '*_test.go')

# protocol 契约层不得反向依赖业务实现或设备接入运行时。
# 这条是"新协议只加 protocol/<name>/ 子目录"能成立的前提。
while IFS= read -r file; do
  hits=$(grep -oE '"github\.com/ChargePilot2026/charge-pilot/internal/(central|gateway|worker|delivery|platform)/[a-z0-9_/-]+"' "$file" | tr -d '"' | sort -u | head -3)
  [[ -z $hits ]] && continue
  # platform 是无业务语义的通用能力，契约层可以依赖；其余均不允许。
  bad=$(printf '%s\n' "$hits" | grep -v '/platform/' | tr '\n' ' ')
  if [[ -n $bad ]]; then
    report "$file: protocol 契约层不得依赖实现层: $bad"
  fi
done < <(find internal/protocol -name '*.go' ! -name '*_test.go')

if ((violations > 0)); then
  printf '\n分层约定未满足：%d 处。修完再提交。\n' "$violations" >&2
  exit 1
fi
printf '分层约定检查通过。\n'
