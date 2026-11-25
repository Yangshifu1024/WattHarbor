// 规则层检查：判定「规则层是否完好」，失败时指明被违反的不变量与涉及的文件。
// 只读文档与迁移 schema，不连数据库、不连网络。
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, normalize, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// ROOT 定位仓库根，使脚本可从任意目录执行。
const ROOT = fileURLToPath(new URL('../../', import.meta.url));

export const RULES_ENTRY = 'AGENTS.md';

// FROZEN_DOCUMENTS 登记仍可查阅但不再更新规则的文档。清单必须与实际冻结的文件同步。
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
      violations.push({ file: doc, message: '顶部缺少冻结声明：标题后须有包含「已冻结」「只读」并链接维护入口的声明' });
      continue;
    }
    if (!declaration.includes('已冻结') || !declaration.includes('只读')) {
      violations.push({ file: doc, message: '冻结声明须含「已冻结」与「只读」字样，让人与 agent 一眼看出它已过期' });
      continue;
    }
    const targets = localTargets(declaration);
    if (targets.length === 0) {
      violations.push({ file: doc, message: '冻结声明须有指向维护入口的链接' });
      continue;
    }
    for (const target of targets) {
      if (!existsSync(join(root, dirname(doc), target))) {
        violations.push({ file: doc, message: `冻结声明的链接指向不存在的文件：${target}` });
      }
    }
    if (!targets.some(target => normalize(join(dirname(doc), target)) === RULES_ENTRY)) {
      violations.push({ file: doc, message: `冻结声明的链接没有指向维护入口 ${RULES_ENTRY}` });
    }
  }
  return violations;
}

// ADR_DIRECTORY 是架构决策目录；目录里的 .md 全部是决策记录，目录说明本身除外。
const ADR_DIRECTORY = 'docs/adr';

// ADR_FILE 约定文件名即编号：四位编号 + 短横线 + 小写短横线 slug。
// 编号写在文件名里，目录本身就是索引，不需要另建一份编号对照表。
const ADR_FILE = /^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$/;

// ADR_STATUSES 是状态取值的固定集合，不是自由文本；docs/adr/README.md 写给人看，
// 这里才是机器认的那一份，改集合要同时改说明。
const ADR_STATUSES = ['proposed', 'accepted', 'rejected', 'deprecated', 'superseded'];

// ADR_STATUS 取文件头 YAML front matter 里的 status 值；位置固定在第一块 front matter。
// 取不到返回 null，与「写了但取值非法」区分开，两者的提示不同。
function adrStatus(source) {
  const frontMatter = source.split(/^---$/m);
  if (frontMatter.length < 3) return null;
  const line = frontMatter[1].split('\n').find(content => content.startsWith('status:'));
  return line ? line.slice('status:'.length).trim() : null;
}

// assertAdrNumbering 判定编号连续：读者靠编号发现「中间缺了一篇」，
// 不会因为编号跳号而误以为某条决策不存在。
function assertAdrNumbering(root) {
  const directory = join(root, ADR_DIRECTORY);
  if (!existsSync(directory)) return [];
  const entries = readdirSync(directory)
    .filter(name => name.endsWith('.md') && name !== 'README.md')
    .map(name => ({ name, match: ADR_FILE.exec(name) }));
  const violations = [];
  const seen = new Map();
  for (const entry of entries) {
    const file = `${ADR_DIRECTORY}/${entry.name}`;
    if (!entry.match) {
      violations.push({ file, message: '文件名不符合 NNNN-<slug>.md 的约定，编号是目录索引的一部分' });
      continue;
    }
    const number = entry.match[1];
    if (seen.has(number)) {
      violations.push({ file, message: `编号 ${number} 重号：与 ${ADR_DIRECTORY}/${seen.get(number)} 重复` });
      continue;
    }
    seen.set(number, entry.name);
    const status = adrStatus(readFileSync(join(directory, entry.name), 'utf8'));
    if (status === null) {
      violations.push({ file, message: '文件头的 front matter 缺少 status 状态标注' });
    } else if (!ADR_STATUSES.includes(status)) {
      violations.push({ file, message: `状态取值「${status}」不在约定集合内：${ADR_STATUSES.join('、')}` });
    }
  }
  for (let index = 1; index <= seen.size; index++) {
    const expected = String(index).padStart(4, '0');
    if (!seen.has(expected)) {
      violations.push({ file: ADR_DIRECTORY, message: `编号断号：缺少 ${expected}，现有编号 ${[...seen.keys()].sort().join('、')}` });
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
  { id: 'frozen-documents', title: '冻结文档顶部带冻结声明并指向维护入口', assert: assertFrozenDeclarations },
  { id: 'adr-numbering', title: '架构决策目录编号连续、每份带状态标注', assert: assertAdrNumbering },
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
