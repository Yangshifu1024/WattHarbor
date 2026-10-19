// 规则层检查的外部行为：判定结果只落在退出码与输出文本上，不绑定脚本内部结构。
// 本文件不连数据库、不连网络、不启动服务。
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, relative } from 'node:path';
import { FROZEN_DOCUMENTS, NEW_LAYER_ENTRY, main } from './check-rules.mjs';

// 记录 main 的全部输出，便于断言报告内容；成功与失败走同一个收集器。
function capture() {
  const lines = [];
  return { lines, options: { log: line => lines.push(String(line)), error: line => lines.push(String(line)) } };
}

test('尚未实现的断言逐条标为尚未实现，不计入已验证', () => {
  const { lines, options } = capture();
  const status = main([], options);
  const output = lines.join('\n');
  assert.equal(status, 0);
  assert.equal(lines.filter(line => line.startsWith('尚未实现：')).length, 5);
  assert.match(output, /0 条失败，5 条尚未实现/);
  assert.doesNotMatch(output, /全部通过/);
});

test('某条不变量被违反时报红，并点名不变量与涉及文件', () => {
  const { lines, options } = capture();
  const violated = [{
    id: 'enum-value-strings',
    title: '数据库枚举的取值串不得出现在任何文档中，只能是指向迁移 schema 的指针',
    assert: () => [{ file: 'docs/business-contracts.md', message: '文档重述了 payment_status 的取值' }],
  }];
  const status = main([], { ...options, invariants: violated });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /数据库枚举的取值串不得出现在任何文档中/);
  assert.match(output, /enum-value-strings/);
  assert.match(output, /docs\/business-contracts\.md/);
});

// 冻结清单直接取检查自身的登记处，避免测试里再抄一份、抄漏了也不会失败。
const FROZEN = FROZEN_DOCUMENTS;

// 声明样板：紧跟标题，含「已冻结」「只读」，并按相对路径链接到新层入口。
// flaw 用来制造检查应当抓到的各类声明缺陷：整体缺失、措辞不对、缺链接、链接落空、指错入口、位置不对。
function declaration(docPath, flaw) {
  const lead = flaw === 'wording' ? '**请注意。** ' : '**已冻结、只读。** ';
  const body = '本文档的规则已迁入新规则层，不再在此新增或修改；现行规则';
  if (flaw === 'nolink') return `> ${lead}${body}见规则层入口。`;
  const target = { link: 'docs/rules/not-there.md', wrongentry: 'docs/other.md' }[flaw] ?? NEW_LAYER_ENTRY;
  return `> ${lead}${body}见 [规则层入口](${relative(dirname(docPath), target)})。`;
}

// 搭一个一次性的假仓库，只含新层入口与冻结文档，让检查对受控的文档集合判定。
async function frozenRepo(t, { flaws = {}, absent = [] } = {}) {
  const root = await mkdtemp(join(tmpdir(), 'check-rules-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(dirname(join(root, NEW_LAYER_ENTRY)), { recursive: true });
  await writeFile(join(root, NEW_LAYER_ENTRY), '# 规则层入口\n');
  // 指错入口这一缺陷需要一个确实存在、只是不是入口的目标，检查才不会靠「文件不存在」蒙混过关。
  await writeFile(join(root, 'docs/other.md'), '# 别的文档\n');
  for (const doc of FROZEN) {
    if (absent.includes(doc)) continue;
    await mkdir(dirname(join(root, doc)), { recursive: true });
    const flaw = flaws[doc];
    const lines = ['# 旧规则文档', ''];
    if (flaw !== 'missing') {
      if (flaw === 'buried') lines.push('这是导语。', '');
      lines.push(declaration(doc, flaw), '');
    }
    lines.push('## 正文', '', '内容。', '');
    await writeFile(join(root, doc), lines.join('\n'));
  }
  return root;
}

test('冻结文档顶部缺少声明时报红，并点名该文档', async (t) => {
  const root = await frozenRepo(t, { flaws: { [FROZEN[1]]: 'missing' } });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /冻结文档顶部带冻结声明并指向新层/);
  assert.match(output, /docs\/business-contracts\.md/);
});

test('冻结声明缺「已冻结」「只读」字样时报红', async (t) => {
  const root = await frozenRepo(t, { flaws: { [FROZEN[2]]: 'wording' } });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /docs\/refactor-roadmap\.md/);
  assert.match(output, /已冻结.*只读/);
});

test('冻结声明没有带链接时报红', async (t) => {
  const root = await frozenRepo(t, { flaws: { [FROZEN[3]]: 'nolink' } });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /docs\/status-enum-roadmap\.md/);
  assert.match(output, /链接/);
});

test('冻结声明的新层入口链接落空时报红', async (t) => {
  const root = await frozenRepo(t, { flaws: { [FROZEN[0]]: 'link' } });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /docs\/architecture-boundaries\.md/);
  assert.match(output, /rules\/not-there\.md/);
});

test('冻结声明指向存在但不是新层入口的文件时报红', async (t) => {
  const root = await frozenRepo(t, { flaws: { [FROZEN[1]]: 'wrongentry' } });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /docs\/business-contracts\.md/);
  assert.match(output, /新层入口/);
});

test('冻结声明被导语挤到标题之下时报红', async (t) => {
  const root = await frozenRepo(t, { flaws: { [FROZEN[0]]: 'buried' } });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /docs\/architecture-boundaries\.md/);
  assert.match(output, /标题/);
});

test('登记的冻结文档已被删除时报红，提示更新清单', async (t) => {
  const root = await frozenRepo(t, { absent: [FROZEN[0]] });
  const { lines, options } = capture();
  const status = main([], { ...options, root });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /docs\/architecture-boundaries\.md/);
  assert.match(output, /FROZEN_DOCUMENTS/);
});

test('冻结文档补上声明后判定为绿，并点名该条不变量已实现', () => {
  const { lines, options } = capture();
  const status = main([], options);
  const output = lines.join('\n');
  assert.equal(status, 0);
  assert.match(output, /通过：冻结文档顶部带冻结声明并指向新层/);
  assert.match(output, /0 条失败，5 条尚未实现/);
  assert.doesNotMatch(output, /失败：\[frozen-documents\]/);
});
