// 规则层检查：判定「规则层是否完好」，失败时指明被违反的不变量与涉及的文件。
// 只读文档与迁移 schema，不连数据库、不连网络。
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, normalize, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// ROOT 定位仓库根，使脚本可从任意目录执行。
const ROOT = fileURLToPath(new URL('../../', import.meta.url));

// NEW_LAYER_ENTRY 是规则层的单一入口；冻结声明必须指向它，指向别的文件不算「指向新层」。
export const NEW_LAYER_ENTRY = 'docs/rules/README.md';

// FROZEN_DOCUMENTS 登记已冻结、只读的旧规则文档：新层立起后，规则一律进新层，
// 旧文档保留可读但不再接受新增规则。清单必须与实际冻结的文件同步。
export const FROZEN_DOCUMENTS = [
  'docs/architecture-boundaries.md',
  'docs/business-contracts.md',
  'docs/refactor-roadmap.md',
  'docs/status-enum-roadmap.md',
];

// 冻结声明必须紧跟在标题之后：跳过标题行与空行，第一行非空内容就得是引用块。
// 锚在结构上而非行号上，重排版不会误报，往导语后面塞声明则会被判不合格。
function freezeDeclaration(source) {
  const afterTitle = source.split('\n').slice(source.startsWith('# ') ? 1 : 0);
  const first = afterTitle.find(line => line.trim().length > 0) ?? '';
  return first.startsWith('> ') ? first : null;
}

// 取出声明里的站内链接，锚点与外链不参与解析。
function localTargets(declaration) {
  return [...declaration.matchAll(/\[[^\]]*\]\(([^)\s]+)\)/g)]
    .map(match => match[1].split('#')[0])
    .filter(target => target.length > 0 && !/^[a-z]+:/i.test(target));
}

function assertFrozenDeclarations(root) {
  const violations = [];
  for (const doc of FROZEN_DOCUMENTS) {
    if (!existsSync(join(root, doc))) {
      violations.push({ file: doc, message: '登记在冻结清单里但文件已不存在：迁移完成后请从 FROZEN_DOCUMENTS 移除该条目' });
      continue;
    }
    const declaration = freezeDeclaration(readFileSync(join(root, doc), 'utf8'));
    if (!declaration) {
      violations.push({ file: doc, message: '顶部缺少冻结声明：首个二级标题之前须给出含「已冻结」「只读」并链接新层入口的声明' });
      continue;
    }
    if (!declaration.includes('已冻结') || !declaration.includes('只读')) {
      violations.push({ file: doc, message: '冻结声明须含「已冻结」与「只读」字样，让人与 agent 一眼看出它已过期' });
      continue;
    }
    const targets = localTargets(declaration);
    if (targets.length === 0) {
      violations.push({ file: doc, message: '冻结声明须带一个指向新层入口的链接，否则读到它的人无处可去' });
      continue;
    }
    for (const target of targets) {
      if (!existsSync(join(root, dirname(doc), target))) {
        violations.push({ file: doc, message: `冻结声明的链接指向不存在的文件：${target}` });
      }
    }
    if (!targets.some(target => normalize(join(dirname(doc), target)) === NEW_LAYER_ENTRY)) {
      violations.push({ file: doc, message: `冻结声明的链接没有指向新层入口 ${NEW_LAYER_ENTRY}，指向别的文档不算「指向新层」` });
    }
  }
  return violations;
}

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
  { id: 'frozen-documents', title: '冻结文档顶部带冻结声明并指向新层', assert: assertFrozenDeclarations },
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
