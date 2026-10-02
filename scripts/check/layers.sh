#!/usr/bin/env bash
# 校验业务包内的分层约定：同一包内按文件后缀区分接口层、持久化层与领域层。
# 目的是让"某个函数属于哪一层"成为可以机检的事实，而不是靠人记。
#
# 三条规则（只对参与分层的包生效）：
#   1. 含 *gin.Context 的文件必须命名为 *_http.go 或 *_middleware.go
#   2. 含 gorm 的文件必须命名为 *_store.go 或 models.go
#   3. 出现数据库表名的文件必须命名为 *_store.go 或 models.go
# 不满足即退出码 1。中间件是接口层设施而非处理器，单独承认 *_middleware.go 这一类。
#
# 不参与分层的目录：platform 是无业务语义的通用能力，cmd 是进程装配，
# protocol 是设备契约，simulator 是联调工具，worker 侧只做消费与投递、
# 不承载业务判定，其 *_sync.go / *_client.go 属服务间调用适配层。
set -uo pipefail
cd "$(dirname "$0")/../.."

# 包级豁免：整个包就是单一层，不按文件后缀要求。
# gateway/store 全部是持久化；gateway/control 全部是设备接口层；
# worker 各包是服务间适配层。
packages=(
  internal/central/identity
  internal/central/pricing
  internal/central/channel
  internal/central/billing
  internal/central/wallet
  internal/central/card
  internal/central/coupon
  internal/central/order
  internal/central/payment
  internal/central/refund
  internal/central/settlement
  internal/central/charge
  internal/central/admin
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

for dir in "${packages[@]}"; do
  [[ -d $dir ]] || continue
  while IFS= read -r file; do
    stem=$(basename "${file%.go}")
    layer=domain
    [[ $stem == *_http || $stem == http || $stem == *_middleware ]] && layer=http
    [[ $stem == *_store || $stem == store || $stem == *_model || $stem == models ]] && layer=store

    if grep -q 'gin\.Context' "$file" && [[ $layer != http ]]; then
      report "$file: 含 gin.Context 但不是 *_http.go（接口层）"
    fi
    if grep -qE 'gorm\.DB|gorm\.Expr|gorm\.DeletedAt|clause\.OnConflict|"gorm\.io/gorm"' "$file" && [[ $layer != store ]]; then
      report "$file: 含 gorm 但不是 *_store.go / models.go（持久化层）"
    fi
    # 表名只认出现在 SQL 构造位置的形式（Table/FROM/JOIN/INTO/UPDATE 后面），
    # 避免把 StopUser = "user"、ExecutorDevice = "device" 这类枚举值误判为表访问。
    hits=$(grep -oE '(Table\(|FROM |JOIN |INTO |UPDATE )`?"?[a-z0-9_]+' "$file" |
      grep -oE '[a-z0-9_]+$' | sort -u | grep -xF -f "$tables" | head -3)
    if [[ -n $hits && $layer != store ]]; then
      report "$file: 出现数据库表名（$(echo "$hits" | tr '\n' ' ')）但不是 *_store.go / models.go"
    fi
  done < <(find "$dir" -name '*.go' ! -name '*_test.go')
done

if ((violations > 0)); then
  printf '\n分层约定未满足：%d 处。修完再提交。\n' "$violations" >&2
  exit 1
fi
printf '分层约定检查通过。\n'
