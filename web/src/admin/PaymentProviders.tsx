import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ApiError, request } from '../api';
import { Modal, Notice, Panel } from '../ui';
import { can } from './types';
import type { AdminPrincipal } from './types';
import type { PaymentProvider, PaymentProviderStatus, PaymentProviderUpdate } from './paymentProviderTypes';

const names = { wechat: '微信支付', alipay: '支付宝' };
const fields: Record<string, string> = {
 app_id: '应用 App ID', merchant_id: '商户号', seller_id: '卖家 UID', certificate_serial: '商户证书序列号',
 private_key_pem: '商户应用私钥', api_v3_key: 'APIv3 密钥', public_key_pem: '支付宝公钥', platform_public_keys: '微信平台可信公钥',
 static_metadata_unavailable: '静态配置元数据不可用',
};
function failure(error: unknown) {
 if (error instanceof ApiError) {
  if (error.status === 409) return '配置版本已变化，请重新加载并核对后再保存。';
  if (error.status === 403) return '当前平台身份没有支付渠道配置权限，请重新核对权限。';
  if (error.status === 400) return '配置未保存。请核对该渠道的身份、完整凭据、可信公钥和操作原因。';
 }
 return '支付配置未能确认，请重新加载当前状态后再操作。';
}
function checkStatus(status: PaymentProviderStatus) {
 if (!status || !Number.isSafeInteger(status.version) || status.version < 0 || !Array.isArray(status.providers) || status.providers.length !== 2 || status.providers[0].id !== 'wechat' || status.providers[1].id !== 'alipay' || !status.store_ready) throw new Error('unavailable');
 return status;
}
export function PaymentProviders({ principal, csrf }: { principal: AdminPrincipal; csrf?: string }) {
 // An authority/session change destroys drafts, including write-only credentials.
 const identity = JSON.stringify([principal.user_id, principal.version, principal.enabled, principal.permissions, csrf]);
 return <ProviderSession key={identity} principal={principal} csrf={csrf} />;
}
function ProviderSession({ principal, csrf }: { principal: AdminPrincipal; csrf?: string }) {
 const [status, setStatus] = useState<PaymentProviderStatus>();
 const [error, setError] = useState('');
 const [loading, setLoading] = useState(true);
 const [revision, setRevision] = useState(0);
 const [editing, setEditing] = useState<PaymentProvider>();
 const [saved, setSaved] = useState(false);
 const [stale, setStale] = useState('');
 const readable = can(principal, 'finance:read');
 useEffect(() => {
  let current = true;
  const controller = new AbortController();
  setLoading(true); setStatus(undefined); setError('');
  if (!readable) { setLoading(false); return; }
  request<PaymentProviderStatus>('/admin/v1/payment-providers', { signal: controller.signal }).then(checkStatus).then(data => {
   if (current) { setStatus(data); setLoading(false); }
  }).catch(e => { if (current) { setError(failure(e)); setLoading(false); } });
  return () => { current = false; controller.abort(); };
 }, [readable, revision]);
 function reload() { setEditing(undefined); setStatus(undefined); setSaved(false); setStale(''); setLoading(true); setRevision(v => v + 1); }
 if (!can(principal, 'finance:read')) return <Notice kind="error">当前平台角色没有支付渠道的读取权限。</Notice>;
 return <>
  <div className="admin-page-heading"><div><h1>支付渠道</h1><p>管理平台收款身份与凭据。保存配置后仍需单独完成真实收款验收。</p></div><button onClick={reload} disabled={loading}>重新加载</button></div>
  {loading ? <p role="status">正在读取支付配置…</p> : error ? <Notice kind="error">{error}</Notice> : status ? <>
   {stale ? <Notice kind="error">{stale} 当前显示上次读取的配置，编辑已暂停。</Notice> : null}
   {saved ? <Notice>支付渠道配置已保存。未执行支付或退款。</Notice> : null}
   <p>配置版本 {status.version} · {status.mode === 'direct' ? '正式支付模式' : status.mode === 'sandbox' ? '测试模式' : '支付已停用'} · {status.configured ? '有可用收款渠道' : '暂无可用收款渠道'}</p>
   {!status.managed ? <Notice>当前使用静态配置或未启用托管配置，仅可查看。</Notice> : !can(principal, 'finance:write') ? <Notice>当前平台角色只有读取权限。</Notice> : !csrf ? <Notice kind="error">安全凭证已失效，请刷新页面。</Notice> : null}
   <div className="admin-provider-grid">{status.providers.map(provider => <Panel key={provider.id} title={names[provider.id]}>
    <p>{provider.enabled ? '已启用' : '已停用'} · {provider.configured ? '凭据已配置' : '凭据未配置'}</p>
    <dl className="admin-info">
     <div><dt>应用 App ID</dt><dd>{provider.app_id || '未填写'}</dd></div>
     {provider.id === 'wechat' ? <><div><dt>商户号</dt><dd>{provider.merchant_id || '未填写'}</dd></div><div><dt>商户证书序列号</dt><dd>{provider.certificate_serial || '未填写'}</dd></div><div><dt>可信平台公钥 ID</dt><dd>{provider.platform_key_ids?.join('、') || '未配置'}</dd></div></> : <div><dt>卖家 UID</dt><dd>{provider.seller_id || '未填写'}</dd></div>}
     <div><dt>商户应用私钥</dt><dd>{provider.private_key_configured ? '已配置' : '未配置'}</dd></div>
     {provider.id === 'wechat' ? <><div><dt>APIv3 密钥</dt><dd>{provider.api_v3_key_configured ? '已配置' : '未配置'}</dd></div><div><dt>微信平台可信公钥</dt><dd>{provider.platform_keys_configured ? '已配置' : '未配置'}</dd></div></> : <div><dt>支付宝公钥</dt><dd>{provider.public_key_configured ? '已配置' : '未配置'}</dd></div>}
     <div className="admin-provider-url"><dt>固定通知地址</dt><dd>{provider.notify_url || '当前模式不提供'}</dd></div>
     {provider.id === 'alipay' ? <div className="admin-provider-url"><dt>固定返回地址</dt><dd>{provider.return_url || '当前模式不提供'}</dd></div> : null}
    </dl>
    {provider.missing_fields?.length ? <p>缺少配置：{provider.missing_fields.map(field => fields[field] || field).join('、')}</p> : null}
    {status.managed && can(principal, 'finance:write') ? <button disabled={!csrf || !!stale} onClick={() => { setSaved(false); setEditing(provider); }}>配置{names[provider.id]}</button> : null}
   </Panel>)}</div>
   {editing ? <ProviderEditor provider={editing} version={status.version} csrf={csrf} onClose={() => setEditing(undefined)} onReload={reload} onUnconfirmed={setStale} onSaved={data => { setStatus(data); setEditing(undefined); setSaved(true); }} /> : null}
  </> : null}
 </>;
}
function ProviderEditor({ provider, version, csrf, onClose, onReload, onUnconfirmed, onSaved }: { provider: PaymentProvider; version: number; csrf?: string; onClose: () => void; onReload: () => void; onUnconfirmed: (message: string) => void; onSaved: (status: PaymentProviderStatus) => void }) {
 const [metadata, setMetadata] = useState({ app_id: provider.app_id, merchant_id: provider.merchant_id, seller_id: provider.seller_id, certificate_serial: provider.certificate_serial });
 const [enabled, setEnabled] = useState(provider.enabled);
 const [keys, setKeys] = useState((provider.platform_key_ids || []).map(id => ({ id, pem: '' })));
 const [privateKey, setPrivateKey] = useState('');
 const [apiKey, setApiKey] = useState('');
 const [publicKey, setPublicKey] = useState('');
 const [reason, setReason] = useState('');
 const [busy, setBusy] = useState(false);
 const [blocked, setBlocked] = useState(false);
 const [error, setError] = useState('');
 const active = useRef(true);
 useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
 function clearSecrets() { setPrivateKey(''); setApiKey(''); setPublicKey(''); setKeys(rows => rows.map(row => ({ ...row, pem: '' }))); }
 function identityChange(field: 'app_id' | 'merchant_id' | 'seller_id', value: string) { clearSecrets(); setMetadata(m => ({ ...m, [field]: value })); }
 async function submit(event: FormEvent) {
  event.preventDefault();
  if (busy || blocked || !csrf) return;
  if (!reason.trim() || new TextEncoder().encode(reason).length > 2000) { setError('操作原因须为 1 至 2000 字节。'); return; }
  const body: PaymentProviderUpdate = { expected_version: version, reason, enabled, app_id: metadata.app_id };
  if (privateKey) body.private_key_pem = privateKey;
  if (provider.id === 'wechat') {
   body.merchant_id = metadata.merchant_id; body.certificate_serial = metadata.certificate_serial;
   body.platform_key_ids = keys.map(key => key.id.trim());
   if (apiKey) body.api_v3_key = apiKey;
   if (keys.some(key => key.pem)) body.platform_public_keys = Object.fromEntries(keys.filter(key => key.pem).map(key => [key.id.trim(), key.pem]));
   if (keys.some(key => !key.id.trim()) || new Set(body.platform_key_ids).size !== keys.length) { setError('可信平台公钥 ID 须填写且不能重复。'); return; }
  } else { body.seller_id = metadata.seller_id; if (publicKey) body.public_key_pem = publicKey; }
  setBusy(true); setError('');
  try {
   const result = checkStatus(await request<PaymentProviderStatus>(`/admin/v1/payment-providers/${provider.id}`, { method: 'PUT', csrf, body }));
   if (active.current) { clearSecrets(); onSaved(result); }
  } catch (e) {
   if (active.current) { clearSecrets(); setError(failure(e));
    const uncertain = !(e instanceof ApiError && e.status === 400);
    setBlocked(uncertain); if (uncertain) onUnconfirmed(failure(e)); }
  } finally { if (active.current) setBusy(false); }
 }
 return <Modal title={`配置${names[provider.id]}`} busy={busy} onClose={onClose}>
  <form className="admin-form admin-provider-form" onSubmit={submit} autoComplete="off">
   <p>基于版本 {version} 保存。凭据留空保留已存值；关闭、切换身份或提交结束后清除输入。</p>
   {provider.id === 'alipay' ? <Notice>使用支付宝公钥模式。填写应用私钥及支付宝公钥。</Notice> : null}
   <fieldset disabled={busy || blocked}>
    <label><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />启用新支付</label>
    <label>应用 App ID<input value={metadata.app_id} maxLength={128} onChange={e => identityChange('app_id', e.target.value)} /></label>
    {provider.id === 'wechat' ? <><label>商户号<input value={metadata.merchant_id} maxLength={128} onChange={e => identityChange('merchant_id', e.target.value)} /></label><label>商户证书序列号<input value={metadata.certificate_serial} maxLength={128} onChange={e => setMetadata(m => ({ ...m, certificate_serial: e.target.value }))} /></label></> : <label>卖家 UID<input value={metadata.seller_id} maxLength={128} onChange={e => identityChange('seller_id', e.target.value)} /></label>}
    <label>商户应用私钥 PEM<textarea value={privateKey} onChange={e => setPrivateKey(e.target.value)} maxLength={65536} autoComplete="off" spellCheck={false} placeholder={provider.private_key_configured ? '已配置，留空保留' : '尚未配置'} /></label>
    {provider.id === 'wechat' ? <>
     <label>APIv3 密钥<input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)} autoComplete="new-password" maxLength={32} placeholder={provider.api_v3_key_configured ? '已配置，留空保留' : '32 字节密钥'} /></label>
     <p>保留轮换期间需要的全部可信公钥 ID。删除 ID 会从当前信任集合移除；替换 ID 后须填写对应公钥。</p>
     {keys.map((key, index) => <div className="admin-provider-key" key={index}>
      <label>可信平台公钥 ID {index + 1}<input value={key.id} maxLength={128} onChange={e => setKeys(rows => rows.map((row, i) => i === index ? { id: e.target.value, pem: '' } : row))} /></label>
      <label>微信平台公钥或证书 PEM {index + 1}<textarea value={key.pem} maxLength={65536} autoComplete="off" spellCheck={false} onChange={e => setKeys(rows => rows.map((row, i) => i === index ? { ...row, pem: e.target.value } : row))} placeholder={provider.platform_key_ids?.includes(key.id) ? '已配置，留空保留' : '填写对应可信公钥'} /></label>
      <button type="button" onClick={() => setKeys(rows => rows.filter((_, i) => i !== index))}>移除公钥 {index + 1}</button>
     </div>)}
     <button type="button" disabled={keys.length >= 16} onClick={() => setKeys(rows => [...rows, { id: '', pem: '' }])}>添加可信平台公钥</button>
    </> : <label>支付宝公钥 PEM<textarea value={publicKey} maxLength={65536} autoComplete="off" spellCheck={false} onChange={e => setPublicKey(e.target.value)} placeholder={provider.public_key_configured ? '已配置，留空保留' : '尚未配置'} /></label>}
    <label>操作原因<textarea value={reason} required maxLength={2000} onChange={e => setReason(e.target.value)} /></label>
   </fieldset>
   {error ? <Notice kind="error">{error}</Notice> : null}
   {blocked ? <button type="button" onClick={onReload}>重新加载并核对</button> : <button className="primary" type="submit" disabled={busy || !csrf}>{busy ? '保存中…' : '保存配置'}</button>}
  </form>
 </Modal>;
}
