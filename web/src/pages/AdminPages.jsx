import React, { useEffect, useState } from 'react';
import { History, Plus, RefreshCw, ShieldAlert } from 'lucide-react';
import { apiRequest, formatDateTime, formatMoney } from '../lib/api.js';

export function AdminAppsPage({ session }) {
  const [apps, setApps] = useState([]);
  const [name, setName] = useState('');
  const [redirectUri, setRedirectUri] = useState('https://app.example.test/auth/callback');
  const [secretBanner, setSecretBanner] = useState('');
  const [errMsg, setErrMsg] = useState('');

  async function load() {
    const res = await apiRequest('/api/admin/apps');
    setApps(res.data.apps ?? []);
  }
  useEffect(() => { load().catch((e) => setErrMsg(e.message)); }, []);

  async function createApp(e) {
    e.preventDefault();
    try {
      const res = await apiRequest('/api/admin/apps', {
        method: 'POST', csrf: session.csrf, body: { name, redirectUris: [redirectUri] },
      });
      setSecretBanner(`应用 ${res.data.app.name} 已创建，Client Secret（仅展示一次）：${res.data.clientSecret}`);
      setName('');
      await load();
    } catch (e2) {
      setErrMsg(e2.message);
    }
  }

  async function rotateSecret(appId) {
    try {
      const res = await apiRequest(`/api/admin/apps/${appId}/rotate-secret`, { method: 'POST', csrf: session.csrf, body: {} });
      setSecretBanner(`新 Client Secret（仅展示一次）：${res.data.clientSecret}`);
    } catch (e2) {
      setErrMsg(e2.message);
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold text-[#142B35]">应用登记与凭证管理</h1>
      {errMsg && <div className="rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-700">{errMsg}</div>}
      {secretBanner && <div className="rounded-xl border border-[#35C4A3] bg-[#35C4A3]/10 p-4 font-mono text-xs break-all">{secretBanner}</div>}

      {session.isPlatformOperator && (
        <form onSubmit={createApp} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 grid gap-3 sm:grid-cols-3 items-end">
          <div>
            <label className="mb-1 block text-xs text-[#142B35]/70">应用名称</label>
            <input value={name} onChange={(e) => setName(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm" required />
          </div>
          <div>
            <label className="mb-1 block text-xs text-[#142B35]/70">回调地址 (Redirect URI)</label>
            <input value={redirectUri} onChange={(e) => setRedirectUri(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm" required />
          </div>
          <button type="submit" className="inline-flex items-center justify-center gap-1 rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">
            <Plus className="h-4 w-4" /> 登记新应用
          </button>
        </form>
      )}

      <div className="space-y-3">
        {apps.map((app) => (
          <div key={app.id} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="font-bold text-[#142B35]">{app.name}</div>
              <div className="font-mono text-xs text-[#142B35]/60">ID: {app.id} · 版本数: {app.publishedVersionCount ?? 0}</div>
            </div>
            {session.isPlatformOperator && (
              <button type="button" onClick={() => rotateSecret(app.id)} className="inline-flex items-center gap-1.5 rounded-xl border border-[#DEE8EC] px-3 py-1.5 text-xs font-medium hover:border-[#087F8C]">
                <RefreshCw className="h-3.5 w-3.5" /> 轮换 Client Secret
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

export function AdminCommercePage({ session }) {
  const [apps, setApps] = useState([]);
  const [appId, setAppId] = useState('');
  const [kind, setKind] = useState('personal');
  const [drafts, setDrafts] = useState([]);
  const [versions, setVersions] = useState([]);
  const [showVersions, setShowVersions] = useState(false);
  const [productType, setProductType] = useState('membership');
  const [billingCycle, setBillingCycle] = useState('monthly');
  const [validDays, setValidDays] = useState(90);
  const [tierId, setTierId] = useState('custom_pro');
  const [priceMinor, setPriceMinor] = useState(9900);
  const [baseCredits, setBaseCredits] = useState(2000);
  const [bonusCredits, setBonusCredits] = useState(500);
  const [displayName, setDisplayName] = useState('Pro 月付');
  const [msg, setMsg] = useState('');

  useEffect(() => {
    apiRequest('/api/admin/apps').then((res) => {
      const list = res.data.apps ?? [];
      setApps(list);
      if (list[0]) setAppId(list[0].id);
    }).catch(() => {});
  }, []);

  async function loadDrafts() {
    if (!appId) return;
    const [d, v] = await Promise.all([
      apiRequest(`/api/admin/apps/${appId}/product-drafts?kind=${kind}`),
      apiRequest(`/api/admin/apps/${appId}/versions?type=commerce`),
    ]);
    setDrafts(d.data.cards ?? []);
    setVersions(v.data.versions ?? []);
  }

  useEffect(() => { loadDrafts().catch(() => {}); }, [appId, kind]);

  async function createCard(e) {
    e.preventDefault();
    const isPack = productType === 'credit_pack';
    const rule = isPack
      ? { priceMinor: Number(priceMinor), packCredits: Number(baseCredits), validDays: Number(validDays), packEligibility: 'active_member' }
      : { billingCycle, priceMinor: Number(priceMinor), baseCredits: Number(baseCredits), bonusCredits: Number(bonusCredits) };
    const benefitText = isPack
      ? `${validDays} 天有效期，到账 ${baseCredits} 积分`
      : `每${billingCycle === 'yearly' ? '年按月' : '月'} ${baseCredits} + ${bonusCredits} 积分`;
    try {
      await apiRequest(`/api/admin/apps/${appId}/product-drafts`, {
        method: 'POST',
        csrf: session.csrf,
        body: {
          accountKind: kind,
          productType,
          tierId,
          tierRank: drafts.length * 10 + 10,
          rule,
          display: { name: displayName, description: `${displayName} ${isPack ? '积分包' : '订阅方案'}`, benefits: [benefitText] },
        },
      });
      setMsg('草稿已新建（未发布状态，不影响线上在售商品）。');
      await loadDrafts();
    } catch (err) {
      setMsg(err.message);
    }
  }

  async function deleteDraft(cardId) {
    try {
      await apiRequest(`/api/admin/apps/${appId}/product-drafts/${cardId}`, {
        method: 'DELETE', csrf: session.csrf,
      });
      setMsg('草稿卡已删除。');
      await loadDrafts();
    } catch (err) {
      setMsg(err.message);
    }
  }

  async function publishSingle(cardId, action) {
    try {
      await apiRequest(`/api/admin/apps/${appId}/product-drafts/${cardId}/${action}`, {
        method: 'POST', csrf: session.csrf, body: {},
      });
      setMsg(action === 'publish' ? '单卡已独立上架生成不可变版本快照。' : '单卡已独立下架，历史订单仍可回读原版本。');
      await loadDrafts();
    } catch (err) {
      setMsg(err.message);
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h1 className="text-xl font-bold text-[#142B35]">商品配置与单卡独立上下架</h1>
        <div className="flex items-center gap-2">
          <select value={appId} onChange={(e) => setAppId(e.target.value)} className="rounded-xl border border-[#DEE8EC] bg-white px-3 py-1.5 text-sm">
            {apps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          <select value={kind} onChange={(e) => setKind(e.target.value)} className="rounded-xl border border-[#DEE8EC] bg-white px-3 py-1.5 text-sm">
            <option value="personal">个人版商品卡</option>
            <option value="enterprise">企业版商品卡</option>
          </select>
          <button type="button" onClick={() => setShowVersions(!showVersions)} className="inline-flex items-center gap-1 rounded-xl border border-[#DEE8EC] bg-white px-3 py-1.5 text-xs font-medium">
            <History className="h-3.5 w-3.5" /> 历史版本 ({versions.length})
          </button>
        </div>
      </div>

      {msg && <div className="rounded-xl border border-[#35C4A3] bg-[#35C4A3]/10 p-3 text-sm">{msg}</div>}

      {showVersions && (
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
          <div className="text-sm font-bold">不可变历史版本快照</div>
          {versions.map((v) => (
            <div key={v.id} className="rounded-xl bg-[#F3F8FA] p-3 text-xs flex justify-between">
              <span className="font-mono">{v.versionCode}</span>
              <span>{v.changeSummary} · {formatDateTime(v.effectiveAt)}</span>
            </div>
          ))}
        </div>
      )}

      <form onSubmit={createCard} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 grid gap-3 sm:grid-cols-4 items-end">
        <div>
          <label className="mb-1 block text-xs">商品类型</label>
          <select value={productType} onChange={(e) => setProductType(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm">
            <option value="membership">订阅会员</option>
            <option value="credit_pack">加量积分包</option>
          </select>
        </div>
        <div><label className="mb-1 block text-xs">Tier ID</label><input value={tierId} onChange={(e) => setTierId(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm" required /></div>
        <div><label className="mb-1 block text-xs">对客名称</label><input value={displayName} onChange={(e) => setDisplayName(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm" required /></div>
        <div><label className="mb-1 block text-xs">单价（最小货币单位）</label><input type="number" min="0" value={priceMinor} onChange={(e) => setPriceMinor(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm" required /></div>
        <div><label className="mb-1 block text-xs">{productType === 'credit_pack' ? '包内积分' : '基础积分'}</label><input type="number" min="0" value={baseCredits} onChange={(e) => setBaseCredits(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm" required /></div>
        {productType === 'membership' ? (
          <>
            <div><label className="mb-1 block text-xs">赠送积分</label><input type="number" min="0" value={bonusCredits} onChange={(e) => setBonusCredits(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm" required /></div>
            <div>
              <label className="mb-1 block text-xs">计费周期</label>
              <select value={billingCycle} onChange={(e) => setBillingCycle(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm">
                <option value="monthly">按月 (monthly)</option>
                <option value="yearly">按年 (yearly)</option>
              </select>
            </div>
          </>
        ) : (
          <div><label className="mb-1 block text-xs">有效天数</label><input type="number" min="1" value={validDays} onChange={(e) => setValidDays(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-2.5 py-1.5 text-sm" required /></div>
        )}
        <button type="submit" className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">新增草稿卡</button>
      </form>

      <div className="grid gap-4 md:grid-cols-2">
        {drafts.map((c) => (
          <div key={c.id} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-3">
            <div className="flex items-center justify-between">
              <div className="font-bold">{c.display?.name} <span className="font-mono text-xs text-[#142B35]/60">({c.tierId})</span></div>
              <span className="rounded-full bg-[#F3F8FA] px-2.5 py-0.5 text-xs font-semibold text-[#087F8C]">{c.publishStatus}</span>
            </div>
            <div className="text-xs text-[#142B35]/70">
              本区定价：{formatMoney(c.rule.priceMinor, session.currency)} · {c.productType === 'credit_pack'
                ? `积分 ${c.rule.packCredits} · ${c.rule.validDays}天`
                : `基础 ${c.rule.baseCredits} + 赠送 ${c.rule.bonusCredits ?? 0} 积分 (${c.rule.billingCycle})`}
            </div>
            <div className="flex flex-wrap gap-2">
              <button type="button" onClick={() => publishSingle(c.id, 'publish')} className="rounded-lg bg-[#087F8C] px-3 py-1.5 text-xs font-semibold text-white">
                {c.publishStatus === 'published' ? '重新发布' : '单卡上架'}
              </button>
              {c.publishStatus === 'published' ? (
                <button type="button" onClick={() => publishSingle(c.id, 'unpublish')} className="rounded-lg border border-red-200 px-3 py-1.5 text-xs text-red-700">
                  单卡下架
                </button>
              ) : (
                <button type="button" onClick={() => deleteDraft(c.id)} className="rounded-lg border border-red-200 px-3 py-1.5 text-xs text-red-700">
                  删除草稿
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

export function AdminPricingPage({ session }) {
  const isUs = session.region === 'us';
  const [apps, setApps] = useState([]);
  const [appId, setAppId] = useState('');
  const [kind, setKind] = useState('personal');
  const [activeVer, setActiveVer] = useState(null);
  const [cpc, setCpc] = useState(isUs ? 1500 : 200);
  const [msg, setMsg] = useState('');

  useEffect(() => {
    apiRequest('/api/admin/apps').then((res) => {
      const list = res.data.apps ?? [];
      setApps(list);
      if (list[0]) setAppId(list[0].id);
    }).catch(() => {});
  }, []);

  async function loadPricing() {
    if (!appId) return;
    const res = await apiRequest(`/api/admin/apps/${appId}/route-pricing?kind=${kind}`);
    setActiveVer(res.data.activeVersion);
    if (res.data.activeVersion?.creditsPerCurrency) {
      setCpc(res.data.activeVersion.creditsPerCurrency);
    } else {
      setCpc(isUs ? 1500 : 200);
    }
  }

  useEffect(() => { loadPricing().catch(() => {}); }, [appId, kind, isUs]);

  async function publishDefaultFiveRoutes() {
    try {
      await apiRequest(`/api/admin/apps/${appId}/route-pricing/publish`, {
        method: 'POST',
        csrf: session.csrf,
        body: {
          accountKind: kind,
          creditsPerCurrency: Number(cpc),
          routes: [
            { modelId: 'text-embedding-v4', routeId: 'ali:text-embedding-v4', operation: 'embedding', unit: '1K tokens', minBillableQty: '0.04', meterVersion: 'm_ali_emb_v4', routerRateVersion: 'r_ali_v1', baseAmountDecimal: isUs ? '0.00007' : '0.0005', unihubMultiplier: '1.00' },
            { modelId: 'gte-rerank-v2', routeId: 'ali:gte-rerank-v2', operation: 'rerank', unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_rerank_v2', routerRateVersion: 'r_ali_v1', baseAmountDecimal: isUs ? '0.00014' : '0.001', unihubMultiplier: '1.10' },
            { modelId: 'seedream-4.0', routeId: 'volc:seedream-4.0', operation: 'image_generation', unit: '张', minBillableQty: '1', meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1', baseAmountDecimal: isUs ? '0.03' : '0.20', unihubMultiplier: '1.15' },
            { modelId: 'qwen3-vl-embedding', routeId: 'ali:qwen3-vl-embedding', operation: 'multimodal_embedding', unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_vl_emb_v1', routerRateVersion: 'r_ali_v1', baseAmountDecimal: isUs ? '0.00028' : '0.002', unihubMultiplier: '1.10' },
            { modelId: 'paddleocr-vl', routeId: 'baidu:paddleocr-vl', operation: 'ocr_parse', unit: '页', minBillableQty: '1', meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1', baseAmountDecimal: isUs ? '0.005' : '0.03', unihubMultiplier: '1.20' },
          ],
        },
      });
      setMsg('已校验最小计费粒度并发布五类云端 API 路由定价版本。');
      await loadPricing();
    } catch (err) {
      setMsg(err.message);
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h1 className="text-xl font-bold text-[#142B35]">4D 模型路由与售价版本管理</h1>
        <div className="flex gap-2">
          <select value={appId} onChange={(e) => setAppId(e.target.value)} className="rounded-xl border border-[#DEE8EC] bg-white px-3 py-1.5 text-sm">
            {apps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          <select value={kind} onChange={(e) => setKind(e.target.value)} className="rounded-xl border border-[#DEE8EC] bg-white px-3 py-1.5 text-sm">
            <option value="personal">个人费率</option>
            <option value="enterprise">企业费率</option>
          </select>
        </div>
      </div>

      {msg && <div className="rounded-xl border border-[#35C4A3] bg-[#35C4A3]/10 p-3 text-sm">{msg}</div>}

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 flex flex-wrap items-end justify-between gap-3">
        <div>
          <label className="mb-1 block text-xs text-[#142B35]/70">每本位币换算积分 (creditsPerCurrency)</label>
          <input type="number" value={cpc} onChange={(e) => setCpc(e.target.value)} className="w-40 rounded-xl border border-[#DEE8EC] px-3 py-1.5 text-sm" />
        </div>
        <button type="button" onClick={publishDefaultFiveRoutes} className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">
          发布全五类云端路由基线定价
        </button>
      </div>

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2 overflow-x-auto">
        <div className="text-xs font-semibold text-[#142B35]/60">当前生效版本：{activeVer?.versionCode ?? '未发布'}</div>
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/60">
              <th className="py-2 pr-3">Model ID</th>
              <th className="py-2 pr-3">Route ID</th>
              <th className="py-2 pr-3">基础单价</th>
              <th className="py-2 pr-3">倍率</th>
              <th className="py-2">最小粒度单位(1e-6)</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[#DEE8EC]">
            {(activeVer?.routesSnapshot ?? []).map((r) => (
              <tr key={r.routeId}>
                <td className="py-2 pr-3 font-mono text-xs">{r.modelId}</td>
                <td className="py-2 pr-3 font-mono text-xs">{r.routeId}</td>
                <td className="py-2 pr-3 tabular-nums">{r.baseAmountDecimal}</td>
                <td className="py-2 pr-3 tabular-nums">{r.unihubMultiplier}x</td>
                <td className="py-2 tabular-nums font-mono text-xs">{r.minLedgerUnits}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export function AdminOperationsPage({ session }) {
  const [overview, setOverview] = useState(null);
  const [recon, setRecon] = useState(null);
  const [diffs, setDiffs] = useState([]);
  const [failedLago, setFailedLago] = useState([]);
  const [settings, setSettings] = useState([]);
  const [settingKey, setSettingKey] = useState('proxy_display_domain');
  const [settingVal, setSettingVal] = useState('gateway.unihub.example');
  const [msg, setMsg] = useState('');

  async function loadAll() {
    const [ov, rc, df, lg, st] = await Promise.all([
      apiRequest('/api/admin/overview'),
      apiRequest('/api/admin/billing/reconciliation'),
      apiRequest('/api/admin/platform-differences'),
      apiRequest('/api/admin/lago/failed'),
      apiRequest('/api/admin/settings'),
    ]);
    setOverview(ov.data);
    setRecon(rc.data);
    setDiffs(df.data.differences ?? []);
    setFailedLago(lg.data.orders ?? []);
    setSettings(st.data.settings ?? []);
  }

  useEffect(() => { loadAll().catch((e) => setMsg(e.message)); }, []);

  async function runAction(url, label) {
    try {
      await apiRequest(url, { method: 'POST', csrf: session.csrf, body: {} });
      setMsg(`${label} 执行完毕。`);
      await loadAll();
    } catch (e) {
      setMsg(e.message);
    }
  }

  async function saveSetting(e) {
    e.preventDefault();
    try {
      await apiRequest('/api/admin/settings', {
        method: 'PUT', csrf: session.csrf, body: { key: settingKey, value: settingVal },
      });
      setMsg('非敏感运行参数已更新。');
      await loadAll();
    } catch (err) {
      setMsg(err.message);
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold text-[#142B35]">平台对账、运维调度与非密钥配置</h1>
      {msg && <div className="rounded-xl border border-[#35C4A3] bg-[#35C4A3]/10 p-3 text-sm">{msg}</div>}

      <div className="grid gap-4 sm:grid-cols-4">
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-4"><div className="text-xs text-[#142B35]/65">现金/积分守恒状态</div><div className="mt-1 text-lg font-bold text-[#087F8C]">{recon?.ok ? '100% 守恒一致' : '异常待核'}</div></div>
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-4"><div className="text-xs text-[#142B35]/65">生效订阅数</div><div className="mt-1 text-lg font-bold">{overview?.activeSubscriptionsCount ?? '--'}</div></div>
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-4"><div className="text-xs text-[#142B35]/65">在途预扣数</div><div className="mt-1 text-lg font-bold">{overview?.openHoldsCount ?? '--'}</div></div>
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-4"><div className="text-xs text-[#142B35]/65">平台差异记录</div><div className="mt-1 text-lg font-bold">{overview?.platformDifferencesCount ?? '--'}</div></div>
      </div>

      <div className="flex flex-wrap gap-3">
        <button type="button" onClick={() => runAction('/api/internal/billing/outbox/process', 'Outbox 队列重放')} className="rounded-xl bg-[#087F8C] px-4 py-2 text-xs font-semibold text-white">执行 Outbox 队列</button>
        <button type="button" onClick={() => runAction('/api/internal/billing/renewals/process', '订阅续费与按月发分')} className="rounded-xl border border-[#DEE8EC] bg-white px-4 py-2 text-xs font-semibold">执行续费检查</button>
        <button type="button" onClick={() => runAction('/api/internal/billing/holds/reconcile', '超期预扣巡检')} className="rounded-xl border border-[#DEE8EC] bg-white px-4 py-2 text-xs font-semibold">巡检超期预扣</button>
      </div>

      {failedLago.length > 0 && (
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
          <div className="text-sm font-bold">Lago 开票同步失败重试队列 ({failedLago.length})</div>
          {failedLago.map((o) => (
            <div key={o.id} className="flex flex-wrap items-center justify-between gap-2 rounded-xl bg-[#F3F8FA] p-3 text-xs">
              <div className="font-mono">
                {o.orderNo} · {formatMoney(o.paidMinor, o.currency)} · {o.lastError || 'sync_failed'}
              </div>
              <button
                type="button"
                onClick={() => runAction(`/api/admin/lago/retry/${o.id}`, `重试开票 ${o.orderNo}`)}
                className="rounded-lg bg-[#087F8C] px-3 py-1 text-xs font-semibold text-white"
              >
                重试开票
              </button>
            </div>
          ))}
        </div>
      )}

      <form onSubmit={saveSetting} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 grid gap-3 sm:grid-cols-3 items-end">
        <div>
          <label className="mb-1 block text-xs">参数键（禁止密钥/证书名）</label>
          <input value={settingKey} onChange={(e) => setSettingKey(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm" required />
        </div>
        <div>
          <label className="mb-1 block text-xs">参数值</label>
          <input value={settingVal} onChange={(e) => setSettingVal(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm" required />
        </div>
        <button type="submit" className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">保存运行配置</button>
      </form>

      {settings.length > 0 && (
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 text-xs space-y-1.5">
          <div className="font-bold text-sm mb-2">已保存非敏感运行配置</div>
          {settings.map((s) => <div key={s.key} className="font-mono">{s.key} = {JSON.stringify(s.value)}</div>)}
        </div>
      )}

      {diffs.length > 0 && (
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
          <div className="flex items-center gap-2 text-sm font-bold"><ShieldAlert className="h-4 w-4 text-amber-600" /> 平台超额/超时成本差异明细</div>
          {diffs.map((d) => (
            <div key={d.id} className="rounded-xl bg-[#F3F8FA] p-3 text-xs font-mono">
              hold={d.holdId} · reason={d.reason} · expected={d.expectedUnits} · settled={d.settledUnits} · diff={d.diffUnits}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
