// Usage in README. Only test doubles listen on loopback; the browser resolves the real hostname locally.
const assert = require('node:assert/strict')
const crypto = require('node:crypto')
const { spawn, execFileSync } = require('node:child_process')
const fs = require('node:fs')
const http = require('node:http')
const https = require('node:https')
const net = require('node:net')
const os = require('node:os')
const path = require('node:path')
const { chromium } = require('playwright')
const { parse } = require('graphql')

const origin = 'https://unihub.quukk.com'
const dest = fs.mkdtempSync(path.join(os.tmpdir(), 'paylab-smoke-'))
const stage = path.resolve(process.argv[2] || path.join(__dirname, 'stage'))
const nginx = process.env.NGINX_BIN || 'nginx'
const orgId = 'b1100000-0000-4000-a000-000000000001'
const token = 'local-lago-only'
const calls = []
const issues = []
const sockets = new Set()
const servers = []
let browser, edge, page

function project(query, data) {
  const { definitions } = parse(query)
  const fragments = new Map(
    definitions
      .filter((d) => d.kind === 'FragmentDefinition')
      .map((f) => [f.name.value, f]),
  )
  function select(set, row) {
    if (row == null || !set) return row ?? null
    if (Array.isArray(row)) return row.map((item) => select(set, item))
    // GraphQL merges fields across fragments before projecting child selections.
    const fields = new Map()
    function collect(selections) {
      for (const sel of selections) {
        if (sel.kind !== 'Field') {
          const part =
            sel.kind === 'FragmentSpread' ? fragments.get(sel.name.value) : sel
          if (
            !part.typeCondition ||
            row.__typename === part.typeCondition.name.value
          )
            collect(part.selectionSet.selections)
          continue
        }
        const alias = sel.alias?.value || sel.name.value
        if (!fields.has(alias))
          fields.set(alias, { name: sel.name.value, selections: [] })
        if (sel.selectionSet)
          fields.get(alias).selections.push(...sel.selectionSet.selections)
      }
    }
    collect(set.selections)
    return Object.fromEntries(
      [...fields].map(([alias, { name, selections }]) => [
        alias,
        select(selections.length ? { selections } : null, row[name]),
      ]),
    )
  }
  return select(
    definitions.find((d) => d.kind === 'OperationDefinition').selectionSet,
    data,
  )
}

const organization = {
  __typename: 'Organization',
  id: orgId,
  slug: 'demo',
  name: '本地测试账套',
  accessibleByCurrentSession: true,
  defaultCurrency: 'CNY',
  timezone: 'TZ_UTC',
  eventsStore: 'postgres',
  featureFlags: [],
  premiumIntegrations: [],
  authenticationMethods: ['email_password'],
  authenticatedMethod: 'email_password',
}
const currentUser = {
  __typename: 'User',
  id: 'local-user',
  email: 'reader@example.test',
  premium: false,
  csAdmin: false,
  organizations: [organization],
  memberships: [
    {
      __typename: 'Membership',
      id: 'local-membership',
      roles: ['finance'],
      organization,
      permissions: new Proxy(
        { __typename: 'Permissions' },
        {
          get: (obj, key) =>
            obj[key] ??
            ['customersView', 'invoicesView', 'plansView'].includes(key),
        },
      ),
    },
  ],
}
const customer = {
  __typename: 'Customer',
  id: 'local-customer',
  externalId: 'DEMO-001',
  displayName: '青山测试客户',
  name: '青山测试客户',
  createdAt: '2026-01-01T00:00:00Z',
  billingEntity: {
    __typename: 'BillingEntity',
    id: 'local-entity',
    code: 'DEMO',
    name: '本地测试主体',
  },
  activeSubscriptionsCount: 0,
  applicableTimezone: 'TZ_UTC',
  currency: 'CNY',
  accountType: 'customer',
  metadata: [],
}
const invoice = {
  __typename: 'Invoice',
  id: 'local-invoice',
  number: 'DEMO-INV-001',
  customer,
  billingEntity: customer.billingEntity,
  invoiceType: 'subscription',
  status: 'finalized',
  paymentStatus: 'succeeded',
  paymentOverdue: false,
  issuingDate: '2026-01-02',
  totalAmountCents: '12300',
  totalPaidAmountCents: '12300',
  totalDueAmountCents: '0',
  currency: 'CNY',
  payments: [],
  errorDetails: [],
}
function collection(type, rows) {
  return {
    __typename: type,
    metadata: {
      __typename: 'InvoiceCollectionMetadata',
      currentPage: 1,
      totalPages: 1,
      totalCount: rows.length,
      totalCountCapped: false,
      hasNextPage: false,
    },
    collection: rows,
  }
}
function json(res, status, body) {
  res
    .writeHead(status, { 'Content-Type': 'application/json' })
    .end(JSON.stringify(body))
}
async function listen(server) {
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  servers.push(server)
  return server.address().port
}
function request(uri, { method = 'GET', headers = {}, body } = {}) {
  return new Promise((resolve, reject) => {
    const req = https.request(
      {
        hostname: '127.0.0.1',
        port: tlsPort,
        servername: 'unihub.quukk.com',
        ca: fs.readFileSync(path.join(dest, 'cert')),
        path: uri,
        method,
        headers: { Host: 'unihub.quukk.com', ...headers },
      },
      (res) => {
        let text = ''
        res.on('data', (chunk) => (text += chunk))
        res.on('end', () =>
          resolve({ status: res.statusCode, headers: res.headers, text }),
        )
      },
    )
    req.on('error', reject)
    req.end(body)
  })
}
let tlsPort
async function run() {
  const upstream = http.createServer(async (req, res) => {
    const { url, headers } = req
    if (url === '/internal/portals/operator') {
      return res
        .writeHead(
          headers.cookie === 'unihub_session=operator'
            ? 204
            : headers.cookie === 'unihub_session=member'
              ? 403
              : 401,
        )
        .end()
    }
    if (url === '/internal/portals/login') {
      return headers['sec-fetch-mode'] === 'navigate'
        ? res.writeHead(302, { Location: '/mock-operator-login' }).end()
        : json(res, 401, { error: 'operator_required' })
    }
    if (url === '/env-config.js') {
      calls.push({ url, headers })
      return res
        .writeHead(200, { 'Content-Type': 'application/javascript' })
        .end(
          "window.APP_ENV='production'; window.LAGO_DEFAULT_UI_LOCALE='zh-CN'; window.API_URL='http://lago-native.invalid';",
        )
    }
    if (url !== '/graphql') return json(res, 404, {})
    let body = ''
    for await (const chunk of req) body += chunk
    const { operationName, query } = JSON.parse(body)
    calls.push({ url, headers, operationName })
    if (operationName === 'loginUser') {
      return json(res, 200, {
        data: { loginUser: { __typename: 'LoginUserPayload', token } },
      })
    }
    if (headers.authorization !== 'Bearer ' + token)
      return json(res, 401, { error: 'lago_required' })
    if (parse(query).definitions.some((d) => d.operation === 'mutation'))
      return json(res, 405, {})
    json(res, 200, {
      data: project(query, {
        __typename: 'Query',
        currentUser,
        currentVersion: {
          __typename: 'Version',
          number: '1.57.0',
          githubUrl: 'https://example.invalid',
        },
        organization: { ...organization, __typename: 'CurrentOrganization' },
        customers: collection('CustomerCollection', [customer]),
        invoices: collection('InvoiceCollection', [invoice]),
        billingEntities: collection('BillingEntityCollection', []),
      }),
    })
  })
  upstream.on('upgrade', (req, socket) => {
    calls.push({ url: req.url, headers: req.headers })
    const key = crypto
      .createHash('sha1')
      .update(
        req.headers['sec-websocket-key'] +
          '258EAFA5-E914-47DA-95CA-C5AB0DC85B11',
      )
      .digest('base64')
    socket.write(
      [
        'HTTP/1.1 101 Switching Protocols',
        'Upgrade: websocket',
        'Connection: Upgrade',
        'Sec-WebSocket-Accept: ' + key,
        'Sec-WebSocket-Protocol: actioncable-v1-json',
        '',
        '',
      ].join('\r\n'),
    )
    sockets.add(socket)
    socket.on('error', () => {})
  })
  const upstreamPort = await listen(upstream)
  const reservation = net.createServer()
  tlsPort = await listen(reservation)
  await new Promise((resolve) => reservation.close(resolve))
  const cert = path.join(dest, 'cert')
  execFileSync(
    'openssl',
    [
      'req',
      '-x509',
      '-newkey',
      'rsa:2048',
      '-nodes',
      '-days',
      '1',
      '-subj',
      '/CN=unihub.quukk.com',
      '-addext',
      'subjectAltName=DNS:unihub.quukk.com',
      '-keyout',
      dest + '/key',
      '-out',
      cert,
    ],
    { stdio: 'ignore' },
  )
  const pin = crypto
    .createHash('sha256')
    .update(
      new crypto.X509Certificate(fs.readFileSync(cert)).publicKey.export({
        format: 'der',
        type: 'spki',
      }),
    )
    .digest('base64')
  fs.symlinkSync(stage, dest + '/paylab')
  const locations = fs
    .readFileSync(path.join(__dirname, 'locations.conf'), 'utf8')
    .replaceAll('/srv/unihub-admin-portals', dest)
    .replaceAll('127.0.0.1:18781', '127.0.0.1:' + upstreamPort)
    .replaceAll('127.0.0.1:18780', '127.0.0.1:' + upstreamPort)
  const shared = fs
    .readFileSync(path.join(__dirname, '../common.conf'), 'utf8')
    .replaceAll('127.0.0.1:14000', '127.0.0.1:' + upstreamPort)
  const rootDist = process.env.LAGO_ROOT_DIST
  const rootServer = rootDist
    ? `server { listen 127.0.0.1:${tlsPort} ssl; server_name lago-root.test; ssl_certificate "${cert}"; ssl_certificate_key "${dest}/key"; root "${path.resolve(rootDist)}"; location = /env-config.js { proxy_pass http://127.0.0.1:${upstreamPort}; } location / { try_files $uri /index.html; } }`
    : ''
  const config = dest + '/nginx.conf'
  fs.writeFileSync(
    config,
    `daemon off; error_log "${dest}/errors.log"; pid "${dest}/nginx.pid"; events {} http { access_log off; types { text/html html; application/javascript js; text/css css; image/svg+xml svg; font/woff2 woff2; application/wasm wasm; } server { listen 127.0.0.1:${tlsPort} ssl; server_name unihub.quukk.com; ssl_certificate "${cert}"; ssl_certificate_key "${dest}/key"; ${shared} ${locations} location / { return 404; } } ${rootServer} }`,
  )
  execFileSync(nginx, ['-t', '-c', config])
  edge = spawn(nginx, ['-c', config], { stdio: 'inherit' })
  for (let i = 0; i < 50; i++) {
    try {
      await request('/')
      break
    } catch (e) {
      if (i === 49) throw e
      await new Promise((resolve) => setTimeout(resolve, 50))
    }
  }
  const cookie = 'unihub_session=operator; other=never-forward'
  const headers = { Cookie: cookie, Origin: origin }
  for (const uri of [
    '/paylab',
    '/paylab/',
    '/paylab/demo/customers',
    '/paylab/assets/no.js',
    '/paylab/env-config.js',
    '/paylab/favicon-prod.svg',
    '/paylab/api/v1/customers',
    '/paylab/api/cable',
    '/paylab/api/graphql',
  ]) {
    const method = uri.endsWith('/graphql') ? 'POST' : 'GET'
    assert.equal(
      (await request(uri, { method, headers: { Origin: origin } })).status,
      401,
      uri,
    )
    assert.equal(
      (
        await request(uri, {
          method,
          headers: { Origin: origin, Cookie: 'unihub_session=member' },
        })
      ).status,
      403,
      uri,
    )
  }
  assert.equal(
    (await request('/paylab/', { headers: { 'Sec-Fetch-Mode': 'navigate' } }))
      .status,
    302,
  )
  assert.equal(
    (await request('/paylab?x=1', { headers })).headers.location,
    '/paylab/?x=1',
  )
  assert.equal(
    (await request('/paylab/api/v1/customers', { headers })).status,
    404,
  )
  assert.equal(
    (await request('/paylab/assets/absent.js', { headers })).status,
    404,
  )
  for (const endpoint of ['graphql', 'cable']) {
    assert.equal(
      (
        await request('/paylab/api/' + endpoint, {
          headers: { ...headers, Origin: 'https://other.invalid' },
          method: endpoint === 'graphql' ? 'POST' : 'GET',
        })
      ).status,
      403,
    )
  }
  const denied = await request('/paylab/api/graphql', {
    method: 'POST',
    headers,
    body: JSON.stringify({
      operationName: 'Probe',
      query: 'query Probe { currentUser { id } }',
    }),
  })
  assert.equal(denied.status, 401)
  assert.equal(JSON.parse(denied.text).error, 'lago_required')
  const html = await request('/paylab/demo/customers', { headers })
  assert.match(
    html.headers['content-security-policy'],
    /frame-ancestors 'none'/,
  )
  assert.match(html.headers['cache-control'], /no-store/)

  browser = await chromium.launch({
    ...(process.env.CHROME ? { executablePath: process.env.CHROME } : {}),
    args: [
      '--no-proxy-server',
      '--ignore-certificate-errors-spki-list=' + pin,
      '--host-resolver-rules=MAP unihub.quukk.com 127.0.0.1:' +
        tlsPort +
        ', MAP lago-root.test 127.0.0.1:' +
        tlsPort +
        ', MAP * ~NOTFOUND',
    ],
  })
  if (rootDist) {
    const rootPage = await browser.newPage()
    await rootPage.goto('https://lago-root.test/')
    await rootPage.waitForURL('https://lago-root.test/login')
    await rootPage.getByRole('link', { name: /忘记/ }).click()
    await rootPage.waitForURL('https://lago-root.test/forgot-password')
    await rootPage.reload()
    await rootPage.getByRole('textbox').first().waitFor()
    assert.equal(await rootPage.locator('base').getAttribute('href'), '/')
    await rootPage.close()
    console.log('PASS default root build login, forgot-password, reload')
  }
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  })
  await ctx.addCookies([
    {
      name: 'unihub_session',
      value: 'operator',
      url: origin,
      httpOnly: true,
      secure: true,
    },
  ])
  await ctx.addInitScript(() =>
    localStorage.setItem('authToken', 'another-application'),
  )
  page = await ctx.newPage()
  page.setDefaultTimeout(12000)
  page.on('pageerror', (err) => issues.push(err.message))
  page.on('console', (msg) => {
    if (['error', 'warning'].includes(msg.type())) issues.push(msg.text())
  })
  page.on('request', (req) => {
    if (
      /^https?:/.test(req.url()) &&
      !req.url().startsWith(origin + '/paylab/')
    )
      issues.push('Unexpected URL ' + req.url())
  })
  page.on('response', (res) => {
    if (res.status() >= 400) issues.push(res.status() + ' ' + res.url())
  })
  const deep = origin + '/paylab/demo/customers?check=paylab#billing'
  await page.goto(deep)
  await page.waitForURL(origin + '/paylab/login')
  await page.getByRole('link', { name: /忘记/ }).click()
  await page.waitForURL(origin + '/paylab/forgot-password')
  await page.reload()
  await page.getByRole('textbox').first().waitFor()
  await page.goto(deep)
  await page.waitForURL(origin + '/paylab/login')
  assert.equal(
    await page
      .getByRole('link', { name: '注册', exact: true })
      .getAttribute('href'),
    '/paylab/sign-up',
  )
  assert.equal(await page.title(), 'Lago')
  assert.equal(await page.locator('html').getAttribute('lang'), 'zh-CN')
  await page.locator('input[type="password"]').waitFor()
  await page.screenshot({ path: dest + '/login.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: dest + '/login-mobile.png' })
  assert(await page.locator('[data-test="submit"]').isVisible())
  await page.setViewportSize({ width: 1440, height: 900 })
  await page
    .locator('input:not([type="password"])')
    .first()
    .fill('reader@example.test')
  await page.locator('input[type="password"]').fill('fixture-password')
  await page.locator('[data-test="submit"]').click()
  await page.getByText('青山测试客户', { exact: true }).first().waitFor()
  assert.equal(page.url(), deep)
  await page.reload()
  await page.getByText('青山测试客户', { exact: true }).first().waitFor()
  await page.screenshot({ path: dest + '/customers.png' })
  await page.locator('a[href="/paylab/demo/invoices"]').first().click()
  await page.waitForURL(origin + '/paylab/demo/invoices')
  await page.getByText('账单', { exact: true }).first().waitFor()
  await Promise.all([
    page.waitForResponse(
      (res) =>
        res.url().endsWith('/api/graphql') &&
        res.request().postDataJSON()?.operationName === 'getInvoicesList',
    ),
    page.reload(),
  ])
  await page.getByText(invoice.number, { exact: true }).first().waitFor()
  await page.screenshot({ path: dest + '/invoices.png' })
  await page.goto(origin + '/paylab/404')
  await page.getByText('返回应用程序', { exact: true }).click()
  await page.waitForURL(origin + '/paylab/demo/**')
  assert.equal(
    await page.evaluate(() => localStorage.getItem('authToken')),
    'another-application',
  )
  await page.evaluate(async () => {
    const socket = new WebSocket(
      'wss://' + location.host + '/paylab/api/cable',
      ['actioncable-v1-json', 'lago-token.local-lago-only'],
    )
    await new Promise((resolve, reject) => {
      socket.onopen = resolve
      socket.onerror = reject
    })
    socket.close()
  })
  assert(
    calls.some(
      (c) =>
        c.operationName === 'customers' &&
        c.headers['x-lago-organization'] === orgId &&
        c.headers.authorization === 'Bearer ' + token,
    ),
  )
  assert(
    calls.some(
      (c) =>
        c.url === '/cable' &&
        c.headers.origin === origin &&
        c.headers['sec-websocket-protocol'].includes('lago-token.' + token),
    ),
  )
  assert(calls.some((c) => c.url === '/env-config.js'))
  assert(calls.every((c) => !c.headers.cookie))
  const knownWarnings = issues.filter((message) => {
    if (
      message.startsWith(
        'Warning: fragment with name ActivityLogDetails already exists.',
      )
    )
      return true
    const match = message.match(/https:\/\/go\.apollo\.dev\/c\/err#(\S+)$/)
    if (!match) return false
    const warning = JSON.parse(decodeURIComponent(match[1]))
    return (
      warning.version === '3.14.1' &&
      warning.message === 103 &&
      ['ApolloClient', 'useLazyQuery'].includes(warning.args[0])
    )
  })
  assert.deepEqual(
    issues.filter((message) => !knownWarnings.includes(message)),
    [],
  )
  console.log(
    'Native Apollo deprecation/fragment warnings:',
    new Set(knownWarnings).size,
  )
  console.log(
    'PASS',
    [...new Set(calls.map((c) => c.operationName || c.url))].join(', '),
  )
}
run()
  .catch(async (err) => {
    console.error(err)
    if (page)
      console.error(
        page.url(),
        (
          await page
            .locator('body')
            .innerText()
            .catch(() => '')
        ).slice(0, 1400),
      )
    console.error('Browser issues:', issues)
    console.error('Operations:', [
      ...new Set(calls.map((c) => c.operationName || c.url)),
    ])
    process.exitCode = 1
  })
  .finally(async () => {
    await browser?.close()
    edge?.kill('SIGTERM')
    for (const socket of sockets) socket.destroy()
    for (const server of servers) server.close()
    console.log('Evidence:', dest)
  })
