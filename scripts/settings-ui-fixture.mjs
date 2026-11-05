#!/usr/bin/env node
// Isolated UI review only: generated records, no proxy, every write rejected.
import {createServer} from 'node:http';
import {readFileSync, existsSync} from 'node:fs';
import {resolve, dirname, extname} from 'node:path';
import {fileURLToPath} from 'node:url';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../web/dist');
if (!existsSync(resolve(root, 'index.html'))) throw new Error('Build first: npm --prefix web run build');
const products = [
  {id: 'superwagie', name: 'SuperWagie', billing_profile: {included_credits: {enabled: true}, usage: {enabled: false, funding_modes: []}}},
  {id: 'llmrouter', name: 'LLMRouter', billing_profile: {included_credits: {enabled: false}, usage: {enabled: true, funding_modes: ['prepaid_cash']}}},
  {id: 'notes', name: '协作笔记'},
];
const offer = {id: 'fixture-year', product_id: 'superwagie', name: '功能会员年付', account_kind: 'both', kind: 'membership', period: 'year', price: '99000000', monthly_credit: '0', entitlements: {workspace: 'enabled'}, rank: 1, validity_months: 0, refund_rule: 'unused_only_v1', status: 'published', sandbox: true};
const mime = {'.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml', '.ico': 'image/x-icon'};
const server = createServer((req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1');
  const context = new URL(req.headers.referer || req.url, 'http://127.0.0.1');
  const mode = context.searchParams.get('fixture') || 'personal';
  const json = (data, status = 200) => {res.writeHead(status, {'content-type': 'application/json', 'cache-control': 'no-store'}); res.end(JSON.stringify(data));};
  if (req.method !== 'GET') return json({error: 'Isolated fixture: all mutations are disabled'}, 405);
  const path = url.pathname;
  if (path === '/auth/session') return json({authenticated: true, sandbox: true, csrf_token: 'fixture-only', user: {id: 'fixture-person', name: '本地演示'}});
  if (path === '/v1/me/profile') return json({id: 'fixture-person', username: 'fixture', display_name: '本地演示', email: 'fixture@example.invalid', version: '1'});
  if (path === '/admin/v1/me') return json({enabled: mode === 'admin', user_id: 'fixture-person', roles: mode === 'admin' ? ['platform_admin'] : [], permissions: mode === 'admin' ? ['*'] : [], version: 1});
  if (path === '/v1/user-management') return json({enabled: true, can_manage: mode === 'admin'});
  if (path === '/v1/accounts') return json([{id: 'A', name: '演示个人账户', kind: 'personal', role: 'owner'}, {id: 'B', name: '演示团队', kind: 'team', role: mode === 'member' ? 'member' : 'owner'}]);
  if (path === '/v1/products') return json(products);
  if (path === '/v1/catalog') return json([offer]);
  if (path === '/v1/capabilities') return json({sandbox: true, real_payments_configured: false, unified_wallet_recharge: true, product_wallet_recharge: true});
  const match = path.match(/^\/v1\/accounts\/([^/]+)\/(.+)$/);
  if (match) {
    const [, id, resource] = match;
    const finance = !(mode === 'member' && id === 'B');
    if (mode === 'error' && ['wallets', 'calls', 'usage/summary', 'orders'].includes(resource)) return json({error: 'fixture_service_unavailable'}, 503);
    if (resource === 'wallets') return json([{product_id: '', asset: 'CNY/6', amount: '100000000'}, {product_id: 'superwagie', asset: 'CNY/6', amount: '0'}, {product_id: 'llmrouter', asset: 'CNY/6', amount: '12345678'}, {product_id: 'notes', asset: 'CNY/6', amount: '3000000'}]);
    if (resource === 'subscriptions') return json([{id: 'fixture-sub', account_id: id, product_id: 'superwagie', offer, version: 1, status: 'active', anchor: '2026-01-01T00:00:00Z', paid_through: '2099-01-01T00:00:00Z'}]);
    if (resource === 'credits') return json([{id: 'fixture-benefits', product_id: 'superwagie', account_id: id, kind: 'addon', available: '5000000', reserved: '0', total: '5000000', frozen: false, starts_at: '2026-01-01T00:00:00Z', expires_at: '2099-01-01T00:00:00Z'}]);
    if (resource === 'operations') return json([{id: 'fixture-uncertain', account_id: id, kind: 'recharge', status: 'UNKNOWN', created_at: new Date().toISOString()}]);
    const scope = {account_id: id, visibility: finance ? 'account' : 'actor', actor_id: finance ? undefined : 'fixture-person'};
    const filter = {from: url.searchParams.get('from'), to: url.searchParams.get('to'), origin_product_id: url.searchParams.get('origin_product_id') || undefined};
    if (resource === 'calls' || resource === 'usage/summary') {
      const calls = [
        {call_id: 'fixture-cash-refund', source_call_id: 'cash-call-1042', source: 'managed_model', origin_product_id: 'superwagie', model: '示例文本模型', asset: 'CNY/6', held: '1000000', charged: '123456', refunded: '876544', status: 'SETTLED', funding_mode: 'prepaid_cash', funding_product_id: 'llmrouter'},
        {call_id: 'fixture-unknown', source_call_id: 'pending-call-1043', source: 'product_runtime', origin_product_id: 'notes', model: '示例图像模型', asset: 'CNY/6', held: '1000000', status: 'UNKNOWN', funding_mode: 'prepaid_cash', funding_product_id: ''},
        {call_id: 'fixture-credit', source: 'product_runtime', origin_product_id: 'superwagie', model: '历史权益调用', asset: 'CREDIT/6', charged: '2000000', refunded: '0', held: '2000000', status: 'SETTLED', funding_mode: 'credits'},
        {call_id: 'fixture-quota', source: 'product_runtime', origin_product_id: 'notes', model: '历史原生调用', asset: 'LR_QUOTA/0', charged: '9007199254740993', refunded: '0', status: 'SETTLED'},
      ].filter(call => !filter.origin_product_id || call.origin_product_id === filter.origin_product_id).map(call => ({...call, scope, financial_details: finance, account_id: 'original-payer-fixture', actor_id: 'fixture-person', operation: 'chat', instance_id: 'local-fixture', created_at: new Date().toISOString(), time_source: 'recorded', price_version: 'fixture-v1', usage_status: call.status === 'UNKNOWN' ? 'UNKNOWN' : 'COMPLETE'}));
      if (resource === 'calls') return json({scope, filter, financial_details: finance, items: calls, total: String(calls.length), unknown_time: '0', has_more: false, as_of: new Date().toISOString(), accuracy: 'fixture', consistency: 'fixture'});
      const totals = {}, refunds = {};
      for (const call of calls) {if (call.charged !== undefined) totals[call.asset] = (BigInt(totals[call.asset] || '0') + BigInt(call.charged)).toString(); if (call.refunded !== undefined) refunds[call.asset] = (BigInt(refunds[call.asset] || '0') + BigInt(call.refunded)).toString();}
      return json({scope, filter, financial_details: finance, calls_total: String(calls.length), settled_calls: String(calls.filter(call => call.status === 'SETTLED').length), pending_calls: '0', unknown_calls: String(calls.filter(call => call.status === 'UNKNOWN').length), missing_usage_calls: '0', released_calls: '0', charges_by_asset: finance ? totals : undefined, refunds_by_asset: finance ? refunds : undefined, usage_complete: false, unknown_time: '0', as_of: new Date().toISOString()});
    }
    return json([]);
  }
  if (path.startsWith('/v1/')) return json([]);
  if (path.startsWith('/admin/v1/')) return json({error: 'Fixture does not simulate admin writes or operations'}, 404);
  const file = resolve(root, path === '/' || !extname(path) ? 'index.html' : `.${path}`);
  if (!file.startsWith(root + '/') || !existsSync(file)) {res.writeHead(404); return res.end('not found');}
  res.writeHead(200, {'content-type': mime[extname(file)] || 'application/octet-stream', 'cache-control': 'no-store'});
  res.end(readFileSync(file));
});
const port = Number(process.argv[2] || 4187);
server.listen(port, '127.0.0.1', () => console.log(`Isolated settings fixture: http://127.0.0.1:${port}/?view=wallets&product=llmrouter`));
