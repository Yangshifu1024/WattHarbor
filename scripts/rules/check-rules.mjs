// 规则层检查：判定「规则层是否完好」，失败时指明被违反的不变量与涉及的文件。
// 只读文档与迁移 schema，不连数据库、不连网络。
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// ROOT 定位仓库根，使脚本可从任意目录执行。
const ROOT = fileURLToPath(new URL('../../', import.meta.url));

// INVARIANTS 是六条文档层不变量的唯一登记处；assert 为 null 表示该断言尚未实现，
// 报告必须显式标注「尚未实现」，不得把它计入已验证而制造假绿。
// 已实现的断言签名是 assert(root)，返回被违反的清单 [{ file, message }]，空数组表示通过；
// file 用相对仓库根的路径，便于维护者直接定位。
// 这里只判定文档层不变量：依赖方向、表归属与接口契约机器判不了，仍由人工核对。
export const INVARIANTS = [
  { id: 'agent-layer-size', title: 'agent 执行层的行数上限（100 行以内）', assert: null },
  { id: 'rule-marker', title: '执行层的规范性语句指向检查入口，或原地标注「本条无对应检查」', assert: null },
  { id: 'enum-value-strings', title: '数据库枚举的取值串不得出现在任何文档中，只能是指向迁移 schema 的指针', assert: null },
  { id: 'cross-doc-links', title: '跨文档引用的文件与锚点均可解析', assert: null },
  { id: 'frozen-documents', title: '冻结文档顶部带冻结声明并指向新层', assert: null },
  { id: 'adr-numbering', title: '架构决策目录编号连续、每份带状态标注', assert: null },
];

// main 逐条判定不变量并报告结果；有违反返回 1，用法错误返回 2，无违反返回 0。
// 尚未实现的断言既不算失败也不算通过，只在报告与摘要里显式列出。
export function main(args = [], {
  log = console.log, error = console.error, root = ROOT, invariants = INVARIANTS,
} = {}) {
  if (args.length > 0) {
    error('用法：node tasks.mjs check-rules');
    return 2;
  }
  const failed = [];
  let unimplemented = 0;
  for (const invariant of invariants) {
    if (!invariant.assert) {
      unimplemented++;
      log(`尚未实现：${invariant.title}`);
      continue;
    }
    const violations = invariant.assert(root) ?? [];
    if (violations.length === 0) {
      log(`通过：${invariant.title}`);
      continue;
    }
    failed.push(invariant);
    error(`失败：[${invariant.id}] ${invariant.title}`);
    for (const violation of violations) error(`  ${violation.file}：${violation.message}`);
  }
  log(`规则层检查：${failed.length} 条失败，${unimplemented} 条尚未实现。`);
  return failed.length > 0 ? 1 : 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = main(process.argv.slice(2));
