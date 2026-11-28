const portals = new Map([
  ['llmrouter', '万流 LLMRouter'],
  ['auth', 'Logto 管理控制台'],
  ['paylab', 'Lago 账单后台'],
]);

export function portalDestination(name) {
  return portals.has(name) ? '/' + name : '/';
}

function loginRequired(request, reply, name) {
  reply.code(401);
  if (!portals.has(name) || request.headers['sec-fetch-mode'] !== 'navigate' ||
      request.headers['sec-fetch-dest'] !== 'document') {
    return reply.send({ error: 'login_required' });
  }
  return reply.type('text/html; charset=utf-8').send(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${portals.get(name)} · UniHub</title><link rel="stylesheet" href="/portal.css">
<main class="portal"><h1>${portals.get(name)}</h1><p>请使用 UniHub 平台管理员账号登录。</p>
<p><a class="action" href="/login?portal=${name}" target="_blank" rel="noopener">登录 UniHub（新窗口）</a></p>
<p>登录成功后会打开后台，也可以刷新本页继续。</p></main></html>`);
}

export function registerAdminPortals(app, auth, readUserSession, isPlatformOperator) {
  app.get('/internal/portals/operator', auth, async (request, reply) =>
    isPlatformOperator(request.auth) ? reply.code(204).send() : reply.code(403).send({ error: 'forbidden' }));
  app.get('/internal/portals/login', async (request, reply) => {
    const name = String(request.headers['x-original-uri'] ?? '').split(/[/?]/)[1];
    return loginRequired(request, reply, name);
  });
  async function wanliu(request, reply) {
    const session = await readUserSession(request);
    if (!session) return loginRequired(request, reply, 'llmrouter');
    if (!isPlatformOperator(session)) return reply.code(403).send({ error: 'forbidden' });
    return reply.header('content-security-policy',
      "default-src 'none'; style-src 'self'; frame-src https://llmrouter.quukk.com; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
      .type('text/html; charset=utf-8').send(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>万流 LLMRouter · UniHub</title><link rel="stylesheet" href="/assets/llmrouter-frame.css">
<iframe title="万流 LLMRouter 管理控制台" src="https://llmrouter.quukk.com/dashboard" referrerpolicy="no-referrer"
sandbox="allow-scripts allow-same-origin allow-forms allow-downloads allow-modals"></iframe></html>`);
  }
  app.get('/llmrouter', wanliu);
  app.get('/llmrouter/', wanliu);
}
