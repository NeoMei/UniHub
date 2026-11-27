const htmlEntities = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

function escape(value) {
  return value.replace(/[&<>"']/g, (character) => htmlEntities[character]);
}

function accountSelector({ accounts, activeAccount, csrf }) {
  const options = accounts.map(({ id, name, kind }) => {
    const label = kind === 'enterprise' ? `企业 · ${name}` : name;
    return `<option value="${escape(id)}"${activeAccount?.id === id ? ' selected' : ''}>${escape(label)}</option>`;
  }).join('');
  return `
    <form class="account-selector" method="post" action="/api/session/account">
      <label for="payer">付款账户</label>
      ${activeAccount ? '' : '<p role="alert">当前付款账户已不可用，请重新选择。</p>'}
      <input type="hidden" name="csrf" value="${escape(csrf)}">
      <div class="account-controls">
        <select id="payer" name="accountId" required>
          ${activeAccount ? '' : '<option value="" selected disabled>选择付款账户</option>'}
          ${options}
        </select>
        <button type="submit">切换</button>
      </div>
    </form>`;
}

export function renderPortalMain(context, failed) {
  const user = context?.user;
  const name = user ? escape(user.name || user.email || '用户') : '';
  return `<main class="portal">
      <header>
        <h1>UniHub</h1>
        ${context ? accountSelector(context) : ''}
      </header>
      ${user ? `
        ${failed ? '<p role="alert">应用登录未完成，请从应用重试。</p>' : ''}
        <p class="identity">${name}</p>
        <details class="enterprise-create">
          <summary>创建企业账户</summary>
          <form method="post" action="/api/accounts/enterprises">
            <input type="hidden" name="csrf" value="${escape(context.csrf)}">
            <label for="company-name">企业名称</label>
            <div class="account-controls">
              <input id="company-name" name="name" required maxlength="160">
              <button type="submit">创建</button>
            </div>
          </form>
        </details>
        <form method="post" action="/logout">
          <input type="hidden" name="csrf" value="${escape(context.csrf)}">
          <button type="submit">退出登录</button>
        </form>` : `
        ${failed ? '<p role="alert">登录未完成，请重试。</p>' : ''}
        <a class="action" href="/login">登录 UniHub</a>`}
    </main>`;
}

export function renderPortal(context, failed) {
  return `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>UniHub</title>
    <link rel="stylesheet" href="/portal.css">
  </head>
  <body>
    ${renderPortalMain(context, failed)}
  </body>
</html>`;
}
