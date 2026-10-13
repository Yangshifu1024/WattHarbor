// 写操作的忙碌状态属于请求本身。隐藏页面不结束请求；旧会话不能清理新会话的操作。
function beginOperation(page, app, field) {
  page._operations ||= {};
  if (page._operations[field] && page._operations[field].session === app._generation) return null;
  const operation = { field, session: app._generation };
  page._operations[field] = operation;
  page.setData({ [field]: true });
  return operation;
}
function operationVisible(page, app, operation) {
  return !page._gone && !page._unloaded && operation.session === app._generation
    && page._operations?.[operation.field] === operation;
}
function finishOperation(page, operation) {
  if (page._operations?.[operation.field] !== operation) return;
  delete page._operations[operation.field];
  if (!page._unloaded) page.setData({ [operation.field]: false });
}
// 回到页面时保留同会话尚未结束的请求；切换会话后丢弃旧操作的界面标记。
function syncOperations(page, app, fields) {
  const state = {};
  for (const field of fields) {
    const operation = page._operations?.[field];
    if (operation && operation.session !== app._generation) delete page._operations[field];
    state[field] = !!page._operations?.[field];
  }
  page.setData(state);
}
export { beginOperation, operationVisible, finishOperation, syncOperations };
