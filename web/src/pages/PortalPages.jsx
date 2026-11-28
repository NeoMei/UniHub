import React, { useEffect, useState } from 'react';
import { AlertTriangle, CheckCircle2, Copy, KeyRound, Plus, RefreshCw, Sparkles, Trash2, Wallet } from 'lucide-react';
import { apiRequest, formatCredits, formatDateTime, formatMoney, ROLE_LABELS } from '../lib/api.js';
import { NavLink } from '../components/Layout.jsx';

function StatusAlert({ message, tone = 'info', onClose }) {
  if (!message) return null;
  const styles = tone === 'error'
    ? 'border-red-200 bg-red-50 text-red-800'
    : 'border-[#35C4A3]/50 bg-[#35C4A3]/10 text-[#142B35]';
  return (
    <div className={`flex items-center justify-between rounded-xl border px-4 py-3 text-sm ${styles}`}>
      <span>{message}</span>
      {onClose && <button type="button" onClick={onClose} className="text-xs font-semibold underline">关闭</button>}
    </div>
  );
}

export function ConsoleOverview({ session, publicApps, onNavigate }) {
  const [wallet, setWallet] = useState(null);
  const [subs, setSubs] = useState([]);
  const [selectedAppId, setSelectedAppId] = useState(publicApps[0]?.id ?? '');
  const [credits, setCredits] = useState(null);

  useEffect(() => {
    if (!selectedAppId && publicApps[0]?.id) setSelectedAppId(publicApps[0].id);
  }, [publicApps, selectedAppId]);

  useEffect(() => {
    let active = true;
    Promise.all([
      apiRequest('/api/wallet'),
      apiRequest('/api/billing/subscriptions'),
    ]).then(([w, s]) => {
      if (!active) return;
      setWallet(w.data.wallet);
      setSubs(s.data.subscriptions ?? []);
    }).catch(() => {});
    return () => { active = false; };
  }, [session.activeAccountId]);

  useEffect(() => {
    if (!selectedAppId) return;
    let active = true;
    apiRequest(`/api/billing/credits?appId=${encodeURIComponent(selectedAppId)}`)
      .then((res) => { if (active) setCredits(res.data.balance); })
      .catch(() => {});
    return () => { active = false; };
  }, [selectedAppId, session.activeAccountId]);

  const activeSubs = subs.filter((s) => s.status === 'active');

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-[#142B35]">控制台概览</h1>
        <p className="text-sm text-[#142B35]/70">
          当前付款账户：<span className="font-semibold">{session.activeAccount?.name}</span>
          {session.activeAccount?.kind === 'enterprise' ? '（企业共享资金与共享积分池）' : '（个人独立账户）'}
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-3">
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
          <div className="flex items-center justify-between text-xs font-medium text-[#142B35]/65">
            <span>可用现金余额</span>
            <Wallet className="h-4 w-4 text-[#087F8C]" />
          </div>
          <div className="text-2xl font-bold tabular-nums text-[#142B35]">
            {wallet ? formatMoney(wallet.availableMinor, session.currency) : '--'}
          </div>
          {wallet && BigInt(wallet.arrearsMinor) > 0n && (
            <div className="text-xs font-medium text-red-600">
              待补足欠款：{formatMoney(wallet.arrearsMinor, session.currency)}
            </div>
          )}
          <NavLink href="/console/wallet" onNavigate={onNavigate} className="inline-block text-xs font-semibold text-[#087F8C] hover:underline">
            去充值 / 查看账单流水 →
          </NavLink>
        </div>

        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
          <div className="flex items-center justify-between text-xs font-medium text-[#142B35]/65">
            <span>有效应用订阅</span>
            <Sparkles className="h-4 w-4 text-[#087F8C]" />
          </div>
          <div className="text-2xl font-bold tabular-nums text-[#142B35]">{activeSubs.length}</div>
          <div className="text-xs text-[#142B35]/65">
            {activeSubs.map((s) => `${s.tierId}${s.seatCount > 1 ? ` (${s.seatCount}席)` : ''}`).join(' · ') || '暂无生效中订阅'}
          </div>
          <NavLink href="/console/store" onNavigate={onNavigate} className="inline-block text-xs font-semibold text-[#087F8C] hover:underline">
            前往应用商店开通 / 升级 →
          </NavLink>
        </div>

        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
          <div className="flex items-center justify-between text-xs font-medium text-[#142B35]/65">
            <span>应用积分池余额</span>
            <select
              value={selectedAppId}
              onChange={(e) => setSelectedAppId(e.target.value)}
              className="rounded border border-[#DEE8EC] bg-[#F3F8FA] px-1.5 py-0.5 text-xs text-[#142B35]"
            >
              {publicApps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          </div>
          <div className="text-2xl font-bold tabular-nums text-[#142B35]">
            {credits ? `${formatCredits(credits.availableUnits)} 积分` : '--'}
          </div>
          <div className="text-xs text-[#142B35]/65 tabular-nums">
            在途预扣：{credits ? formatCredits(credits.heldUnits) : '0'} · 已结算：{credits ? formatCredits(credits.settledUnits) : '0'}
          </div>
        </div>
      </div>
    </div>
  );
}

export function WalletPage({ session, onNavigate }) {
  const [wallet, setWallet] = useState(null);
  const [orders, setOrders] = useState([]);
  const [ledger, setLedger] = useState([]);
  const [amountYuan, setAmountYuan] = useState('100');
  const [provider, setProvider] = useState(session.region === 'us' ? 'stripe' : 'wechat');
  const [statusMsg, setStatusMsg] = useState(null);
  const [checkoutSession, setCheckoutSession] = useState(null);
  const [pendingCheckout, setPendingCheckout] = useState(null);

  async function refresh() {
    const [w, po, l] = await Promise.all([
      apiRequest('/api/wallet'),
      apiRequest('/api/wallet/payment-orders'),
      apiRequest('/api/wallet/ledger'),
    ]);
    setWallet(w.data.wallet);
    setOrders(po.data.paymentOrders ?? []);
    setLedger(l.data.entries ?? []);
    const withPending = (po.data.paymentOrders ?? []).find((o) => o.pendingCheckout && o.status === 'succeeded');
    setPendingCheckout(withPending?.pendingCheckout ?? null);
  }

  useEffect(() => {
    refresh().catch((err) => setStatusMsg({ tone: 'error', text: err.message }));
  }, [session.activeAccountId]);

  async function createRecharge(e) {
    e.preventDefault();
    try {
      const amountMinor = Math.round(Number(amountYuan) * 100);
      const res = await apiRequest('/api/wallet/recharges', {
        method: 'POST',
        csrf: session.csrf,
        body: { provider, amountMinor },
      });
      setCheckoutSession(res.data.checkoutSession ?? null);
      setStatusMsg({ tone: 'info', text: '充值订单已生成，请使用下方渠道收银台完成支付并等待回调确认。' });
      await refresh();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function sandboxConfirm(paymentOrderId) {
    try {
      await apiRequest(`/api/wallet/payment-orders/${paymentOrderId}/sandbox-settle`, {
        method: 'POST',
        csrf: session.csrf,
        body: { providerTradeNo: `ui-sb-${Date.now()}` },
      });
      setStatusMsg({ tone: 'info', text: '测试渠道入账成功，余额与现金流水已原子更新。' });
      await refresh();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function resumePendingCheckout() {
    if (!pendingCheckout) return;
    try {
      const endpoint = pendingCheckout.orderType === 'upgrade' ? '/api/billing/upgrades' : '/api/billing/checkout';
      const body = pendingCheckout.orderType === 'upgrade'
        ? { appId: pendingCheckout.appId, targetTierId: pendingCheckout.tierId, targetVersionId: pendingCheckout.versionId }
        : pendingCheckout;
      await apiRequest(endpoint, { method: 'POST', csrf: session.csrf, body });
      setStatusMsg({ tone: 'info', text: '待购商品已二次确认购买并提交权益发放！' });
      setPendingCheckout(null);
      await refresh();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  const providers = session.region === 'us'
    ? [{ value: 'stripe', label: 'Stripe (USD)' }]
    : [{ value: 'wechat', label: '微信支付 (CNY)' }, { value: 'alipay', label: '支付宝 (CNY)' }];

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold text-[#142B35]">现金钱包与充值</h1>
      {statusMsg && <StatusAlert message={statusMsg.text} tone={statusMsg.tone} onClose={() => setStatusMsg(null)} />}

      {checkoutSession && (
        <div className="rounded-2xl border border-[#087F8C] bg-white p-5 space-y-2">
          <div className="text-sm font-bold text-[#142B35]">支付收银台会话已创建（{checkoutSession.provider}）</div>
          {checkoutSession.codeUrl && (
            <div className="rounded-xl bg-[#F3F8FA] p-3 font-mono text-xs break-all">微信扫码链接：{checkoutSession.codeUrl}</div>
          )}
          {checkoutSession.redirectUrl && (
            <a href={checkoutSession.redirectUrl} target="_blank" rel="noreferrer" className="inline-block rounded-xl bg-[#087F8C] px-4 py-2 text-xs font-semibold text-white">
              前往官方收银台完成付款
            </a>
          )}
        </div>
      )}

      {pendingCheckout && (
        <div className="rounded-2xl border-2 border-[#087F8C] bg-[#35C4A3]/10 p-5 flex flex-wrap items-center justify-between gap-4">
          <div className="space-y-1">
            <div className="text-sm font-bold text-[#142B35]">检测到余额不足跳充值前保留的待确认商品</div>
            <div className="text-xs text-[#142B35]/75">
              套餐：<span className="font-mono">{pendingCheckout.tierId}</span> · 数量：{pendingCheckout.quantity} · 请确认从最新余额扣款完成购买
            </div>
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={resumePendingCheckout}
              className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white hover:bg-[#066973]"
            >
              确认支付完成下单
            </button>
            <NavLink href="/console/store" onNavigate={onNavigate} className="rounded-xl border border-[#DEE8EC] bg-white px-4 py-2 text-sm text-[#142B35]">
              回商店
            </NavLink>
          </div>
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-[1fr_1fr]">
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-4">
          <div className="text-xs font-medium text-[#142B35]/65">当前可用余额（{session.activeAccount?.name}）</div>
          <div className="text-3xl font-bold tabular-nums text-[#142B35]">
            {wallet ? formatMoney(wallet.availableMinor, session.currency) : '--'}
          </div>
          {wallet?.frozenReason && (
            <div className="rounded-xl border border-red-200 bg-red-50 p-3 text-xs text-red-700">
              账户受限（{wallet.frozenReason}），需补齐欠款 {formatMoney(wallet.arrearsMinor, session.currency)} 后自动解冻。
            </div>
          )}
        </div>

        <form onSubmit={createRecharge} className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-4">
          <div className="text-sm font-bold text-[#142B35]">发起充值</div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <label className="mb-1 block text-xs text-[#142B35]/70">充值金额 ({session.currency})</label>
              <input
                type="number"
                min="1"
                step="0.01"
                value={amountYuan}
                onChange={(e) => setAmountYuan(e.target.value)}
                className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm"
                required
              />
            </div>
            <div>
              <label className="mb-1 block text-xs text-[#142B35]/70">本区支付通道</label>
              <select
                value={provider}
                onChange={(e) => setProvider(e.target.value)}
                className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm"
              >
                {providers.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
              </select>
            </div>
          </div>
          <button type="submit" className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white hover:bg-[#066973]">
            创建充值单
          </button>
        </form>
      </div>

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3">
        <h2 className="text-sm font-bold text-[#142B35]">近期充值订单</h2>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/60">
                <th className="py-2 pr-3">通道</th>
                <th className="py-2 pr-3">金额</th>
                <th className="py-2 pr-3">状态</th>
                <th className="py-2 pr-3">创建时间</th>
                <th className="py-2">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#DEE8EC]">
              {orders.map((o) => (
                <tr key={o.id}>
                  <td className="py-2 pr-3 uppercase text-xs font-semibold">{o.provider}</td>
                  <td className="py-2 pr-3 tabular-nums">{formatMoney(o.amountMinor, o.currency)}</td>
                  <td className="py-2 pr-3 text-xs">{o.status}</td>
                  <td className="py-2 pr-3 text-xs text-[#142B35]/65">{formatDateTime(o.createdAt)}</td>
                  <td className="py-2">
                    {o.status === 'pending' && (
                      <button
                        type="button"
                        onClick={() => sandboxConfirm(o.id)}
                        className="rounded-lg border border-[#087F8C] px-2.5 py-1 text-xs font-medium text-[#087F8C] hover:bg-[#087F8C]/10"
                      >
                        沙箱确认入账
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3">
        <h2 className="text-sm font-bold text-[#142B35]">不可变现金总账流水</h2>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/60">
                <th className="py-2 pr-3">类型</th>
                <th className="py-2 pr-3">变动</th>
                <th className="py-2 pr-3">结余</th>
                <th className="py-2">时间</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#DEE8EC]">
              {ledger.map((e) => (
                <tr key={e.id}>
                  <td className="py-2 pr-3 font-mono text-xs">{e.entryType}</td>
                  <td className="py-2 pr-3 tabular-nums font-medium">{formatMoney(e.deltaMinor, e.currency)}</td>
                  <td className="py-2 pr-3 tabular-nums">{formatMoney(e.balanceAfterMinor, e.currency)}</td>
                  <td className="py-2 text-xs text-[#142B35]/65">{formatDateTime(e.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

export function StorePage({ session, publicApps, onNavigate }) {
  const [appId, setAppId] = useState(publicApps[0]?.id ?? '');
  const [storefront, setStorefront] = useState(null);
  const [subs, setSubs] = useState([]);
  const [quantity, setQuantity] = useState(2);
  const [upgradeQuote, setUpgradeQuote] = useState(null);
  const [expandQuote, setExpandQuote] = useState(null);
  const [lowBalanceInfo, setLowBalanceInfo] = useState(null);
  const [statusMsg, setStatusMsg] = useState(null);

  useEffect(() => {
    if (!appId && publicApps[0]?.id) setAppId(publicApps[0].id);
  }, [publicApps, appId]);

  async function loadStore() {
    if (!appId) return;
    const [sf, subRes] = await Promise.all([
      apiRequest(`/api/storefront/products?appId=${encodeURIComponent(appId)}`),
      apiRequest('/api/billing/subscriptions'),
    ]);
    setStorefront(sf.data);
    setSubs(subRes.data.subscriptions ?? []);
  }

  useEffect(() => {
    loadStore().catch((err) => setStatusMsg({ tone: 'error', text: err.message }));
  }, [appId, session.activeAccountId]);

  const currentSub = subs.find((s) => s.appId === appId && s.status === 'active') ?? null;

  async function handleBuy(card) {
    setLowBalanceInfo(null);
    try {
      const qty = session.activeAccount?.kind === 'enterprise' && card.productType === 'membership' ? Number(quantity) : 1;
      await apiRequest('/api/billing/checkout', {
        method: 'POST',
        csrf: session.csrf,
        body: { appId, versionId: storefront.version.id, tierId: card.tierId, quantity: qty, orderType: card.productType === 'credit_pack' ? 'credit_pack' : 'subscribe' },
      });
      setStatusMsg({ tone: 'info', text: `已提交购买「${card.display.name}」，积分权益正在发放。` });
      await loadStore();
    } catch (err) {
      if (err.status === 402) {
        setLowBalanceInfo(err.data);
      } else {
        setStatusMsg({ tone: 'error', text: err.message });
      }
    }
  }

  async function previewUpgrade(card) {
    try {
      const res = await apiRequest('/api/billing/upgrades/quote', {
        method: 'POST',
        csrf: session.csrf,
        body: { appId, targetTierId: card.tierId, targetVersionId: storefront.version.id },
      });
      setUpgradeQuote({ card, quote: res.data });
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function confirmUpgrade() {
    if (!upgradeQuote) return;
    try {
      const res = await apiRequest('/api/billing/upgrades', {
        method: 'POST',
        csrf: session.csrf,
        body: { appId, targetTierId: upgradeQuote.card.tierId, targetVersionId: storefront.version.id },
      });
      setStatusMsg({
        tone: 'info',
        text: `升级完成！已折算返还 ${formatMoney(res.data.returnedMinor, session.currency)} 至原钱包。`,
      });
      setUpgradeQuote(null);
      await loadStore();
    } catch (err) {
      if (err.status === 402) {
        setUpgradeQuote(null);
        setLowBalanceInfo(err.data);
      } else {
        setStatusMsg({ tone: 'error', text: err.message });
      }
    }
  }

  async function previewExpand() {
    if (!currentSub) return;
    try {
      const res = await apiRequest(`/api/billing/subscriptions/${currentSub.id}/seats/expand-quote`, {
        method: 'POST',
        csrf: session.csrf,
        body: { deltaSeats: Number(quantity) },
      });
      setExpandQuote(res.data);
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function confirmExpand() {
    if (!currentSub || !expandQuote) return;
    try {
      await apiRequest(`/api/billing/subscriptions/${currentSub.id}/seats/expand`, {
        method: 'POST',
        csrf: session.csrf,
        body: { deltaSeats: expandQuote.deltaSeats },
      });
      setStatusMsg({ tone: 'info', text: `企业已增购 ${expandQuote.deltaSeats} 席并按剩余周期补发共享积分！` });
      setExpandQuote(null);
      await loadStore();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-bold text-[#142B35]">应用商店与订阅升级</h1>
          <p className="text-sm text-[#142B35]/70">展示当前生效商品版本快照，绝不混入未上架草稿</p>
        </div>
        <select
          value={appId}
          onChange={(e) => setAppId(e.target.value)}
          className="rounded-xl border border-[#DEE8EC] bg-white px-3.5 py-2 text-sm font-medium"
        >
          {publicApps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
      </div>

      {statusMsg && <StatusAlert message={statusMsg.text} tone={statusMsg.tone} onClose={() => setStatusMsg(null)} />}

      {lowBalanceInfo && (
        <div className="rounded-2xl border border-amber-300 bg-amber-50 p-5 flex flex-wrap items-center justify-between gap-4">
          <div className="space-y-1 text-sm text-amber-900">
            <div className="font-bold flex items-center gap-2"><AlertTriangle className="h-4 w-4" /> 余额不足，已保留待购上下文</div>
            <div>需支付 {formatMoney(lowBalanceInfo.amountMinor, session.currency)}，当前可用 {formatMoney(lowBalanceInfo.availableMinor, session.currency)}。</div>
          </div>
          <NavLink href="/console/wallet" onNavigate={onNavigate} className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">
            前往充值后继续购买
          </NavLink>
        </div>
      )}

      {session.activeAccount?.kind === 'enterprise' && (
        <div className="rounded-2xl border border-[#DEE8EC] bg-white p-4 flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <label className="text-xs font-medium text-[#142B35]/75">席位数量</label>
            <input
              type="number"
              min="1"
              value={quantity}
              onChange={(e) => setQuantity(Math.max(1, Number(e.target.value) || 1))}
              className="w-24 rounded-lg border border-[#DEE8EC] px-2.5 py-1 text-sm"
            />
          </div>
          {currentSub && (
            <button
              type="button"
              onClick={previewExpand}
              className="rounded-xl bg-[#087F8C] px-4 py-2 text-xs font-semibold text-white hover:bg-[#066973]"
            >
              现有 {currentSub.seatCount} 席 → 计算追加 {quantity} 席补差
            </button>
          )}
        </div>
      )}

      {upgradeQuote && (
        <div className="rounded-2xl border-2 border-[#087F8C] bg-white p-5 space-y-3">
          <div className="text-sm font-bold text-[#142B35]">个人套餐立即升级折算报价：{upgradeQuote.card.display.name}</div>
          <div className="grid gap-2 text-xs text-[#142B35]/80 sm:grid-cols-3">
            <div>新套餐全价：{formatMoney(upgradeQuote.quote.newPriceMinor, session.currency)}</div>
            <div>预计立即折算返现：{formatMoney(upgradeQuote.quote.estimatedReturnNowMinor, session.currency)}</div>
            <div>{upgradeQuote.quote.hasInFlightHolds ? '含在途调用，终结后将按实耗自动补返差额' : '无在途调用，升级即时完成封账'}</div>
          </div>
          <div className="flex gap-2">
            <button type="button" onClick={confirmUpgrade} className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">确认升级</button>
            <button type="button" onClick={() => setUpgradeQuote(null)} className="rounded-xl border border-[#DEE8EC] px-4 py-2 text-sm">取消</button>
          </div>
        </div>
      )}

      {expandQuote && (
        <div className="rounded-2xl border-2 border-[#087F8C] bg-white p-5 space-y-3">
          <div className="text-sm font-bold text-[#142B35]">企业增席按剩余周期补差确认</div>
          <div className="grid gap-2 text-xs text-[#142B35]/80 sm:grid-cols-3">
            <div>增席数：{expandQuote.currentSeatCount} → {expandQuote.newSeatCount} 席</div>
            <div>本次补差：{formatMoney(expandQuote.deltaMinor, session.currency)}</div>
            <div>立即补发基础积分：{formatCredits(expandQuote.baseGrantUnits)}</div>
          </div>
          <div className="flex gap-2">
            <button type="button" onClick={confirmExpand} className="rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">确认增席</button>
            <button type="button" onClick={() => setExpandQuote(null)} className="rounded-xl border border-[#DEE8EC] px-4 py-2 text-sm">取消</button>
          </div>
        </div>
      )}

      <div className="grid gap-5 md:grid-cols-2 lg:grid-cols-3">
        {(storefront?.cards ?? []).map((card) => {
          const isCurrent = currentSub?.tierId === card.tierId;
          const canUpgrade = Boolean(
            session.activeAccount?.kind === 'personal'
            && currentSub
            && card.productType === 'membership'
            && ((card.tierRank > currentSub.tierRank)
              || (card.tierRank === currentSub.tierRank && currentSub.billingCycle === 'monthly' && card.rule.billingCycle === 'yearly' && card.tierId !== currentSub.tierId)),
          );
          return (
            <div key={card.cardId} className="flex flex-col justify-between rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-4">
              <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-base font-bold text-[#142B35]">{card.display.name}</span>
                  {isCurrent && <span className="rounded-full bg-[#35C4A3]/20 px-2.5 py-0.5 text-xs font-semibold text-[#087F8C]">当前套餐</span>}
                </div>
                <p className="text-xs text-[#142B35]/70">{card.display.description}</p>
                <div className="pt-1 text-2xl font-bold tabular-nums text-[#087F8C]">
                  {formatMoney(card.rule.priceMinor, session.currency)}
                  <span className="text-xs font-normal text-[#142B35]/60">
                    {card.productType === 'credit_pack' ? ` / ${card.rule.validDays}天` : ` / ${card.rule.billingCycle === 'yearly' ? '年' : '月'}`}
                  </span>
                </div>
                <ul className="space-y-1.5 pt-2 text-xs text-[#142B35]/80">
                  {(card.display.benefits ?? []).map((b) => (
                    <li key={b} className="flex items-start gap-1.5">
                      <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[#35C4A3]" />
                      <span>{b}</span>
                    </li>
                  ))}
                </ul>
              </div>
              <div className="pt-2">
                {canUpgrade ? (
                  <button
                    type="button"
                    onClick={() => previewUpgrade(card)}
                    className="w-full rounded-xl bg-[#087F8C] px-4 py-2.5 text-sm font-semibold text-white hover:bg-[#066973]"
                  >
                    升级至此档位（计算折算）
                  </button>
                ) : (
                  <button
                    type="button"
                    disabled={card.productType === 'membership' && Boolean(currentSub)}
                    onClick={() => handleBuy(card)}
                    className="w-full rounded-xl bg-[#087F8C] px-4 py-2.5 text-sm font-semibold text-white hover:bg-[#066973] disabled:cursor-not-allowed disabled:bg-[#DEE8EC] disabled:text-[#142B35]/50"
                  >
                    {card.productType === 'credit_pack' ? '购买积分包' : currentSub ? (isCurrent ? '使用中' : '已有订阅') : '立即开通'}
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

export function ModelsPage({ session, publicApps }) {
  const [appId, setAppId] = useState(publicApps[0]?.id ?? '');
  const [catalog, setCatalog] = useState(null);

  useEffect(() => {
    if (!appId && publicApps[0]?.id) setAppId(publicApps[0].id);
  }, [publicApps, appId]);

  useEffect(() => {
    if (!appId) return;
    apiRequest(`/api/catalog/models?appId=${encodeURIComponent(appId)}`)
      .then((res) => setCatalog(res.data))
      .catch(() => setCatalog(null));
  }, [appId, session.activeAccountId]);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-bold text-[#142B35]">同源模型目录</h1>
          <p className="text-sm text-[#142B35]/70">按当前付款账户、地区、企业应用席位与生效售价版本实时解析，与网关校验保持完全一致</p>
        </div>
        <select value={appId} onChange={(e) => setAppId(e.target.value)} className="rounded-xl border border-[#DEE8EC] bg-white px-3.5 py-2 text-sm">
          {publicApps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
      </div>

      {catalog && !catalog.hasAppSeat && (
        <div className="rounded-2xl border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900">
          当前企业在所选应用下尚未为您分配有效席位，请联系企业管理员授权后再调用模型目录。
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        {(catalog?.models ?? []).map((model) => (
          <div key={model.modelId} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-2">
            <div className="flex items-center justify-between">
              <span className="font-mono text-sm font-bold text-[#142B35]">{model.modelId}</span>
              <span className="rounded bg-[#F3F8FA] px-2 py-0.5 text-xs text-[#087F8C]">{(model.operations ?? []).join(', ')}</span>
            </div>
            {(model.pricing ?? []).map((p, idx) => (
              <div key={idx} className="text-sm font-medium text-[#142B35]/80 tabular-nums">{p.display}</div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

export function KeysPage({ session, publicApps }) {
  const [appId, setAppId] = useState(publicApps[0]?.id ?? '');
  const [keys, setKeys] = useState([]);
  const [keyName, setKeyName] = useState('工作流统一密钥');
  const [createdPlain, setCreatedPlain] = useState('');
  const [statusMsg, setStatusMsg] = useState(null);

  useEffect(() => {
    if (!appId && publicApps[0]?.id) setAppId(publicApps[0].id);
  }, [publicApps, appId]);

  async function loadKeys() {
    if (!appId) return;
    const res = await apiRequest(`/api/keys?appId=${encodeURIComponent(appId)}`);
    setKeys(res.data.keys ?? []);
  }

  useEffect(() => {
    loadKeys().catch((err) => setStatusMsg({ tone: 'error', text: err.message }));
  }, [appId, session.activeAccountId]);

  async function createKey(e) {
    e.preventDefault();
    try {
      const res = await apiRequest('/api/keys', { method: 'POST', csrf: session.csrf, body: { appId, name: keyName } });
      setCreatedPlain(res.data.plaintextKey);
      setStatusMsg({ tone: 'info', text: 'API Key 已签发，请立即复制保存完整明文（系统仅展示一次）。' });
      await loadKeys();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.code === 'app_seat_required' ? '企业账户下必须先拥有本应用席位才可签发 API Key。' : err.message });
    }
  }

  async function rotateKey(keyId) {
    try {
      const res = await apiRequest(`/api/keys/${keyId}/rotate`, { method: 'POST', csrf: session.csrf, body: {} });
      setCreatedPlain(res.data.plaintextKey);
      setStatusMsg({ tone: 'info', text: '旧密钥已吊销，新密钥即时生效。' });
      await loadKeys();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function revokeKey(keyId) {
    try {
      await apiRequest(`/api/keys/${keyId}`, { method: 'DELETE', csrf: session.csrf });
      await loadKeys();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h1 className="text-xl font-bold text-[#142B35]">统一 API Key 管理</h1>
        <select value={appId} onChange={(e) => setAppId(e.target.value)} className="rounded-xl border border-[#DEE8EC] bg-white px-3.5 py-2 text-sm">
          {publicApps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
      </div>

      {statusMsg && <StatusAlert message={statusMsg.text} tone={statusMsg.tone} onClose={() => setStatusMsg(null)} />}

      {createdPlain && (
        <div className="rounded-2xl border-2 border-[#35C4A3] bg-white p-5 space-y-2">
          <div className="text-xs font-semibold text-[#087F8C]">一次性明文 API Key（关闭后无法再次查看）</div>
          <div className="flex items-center justify-between gap-3 rounded-xl bg-[#F3F8FA] p-3 font-mono text-xs break-all">
            <span>{createdPlain}</span>
            <button
              type="button"
              onClick={() => navigator.clipboard?.writeText(createdPlain)}
              className="inline-flex shrink-0 items-center gap-1 rounded-lg bg-[#087F8C] px-2.5 py-1 text-white"
            >
              <Copy className="h-3.5 w-3.5" /> 复制
            </button>
          </div>
        </div>
      )}

      <form onSubmit={createKey} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[200px]">
          <label className="mb-1 block text-xs text-[#142B35]/70">新 API Key 名称</label>
          <input value={keyName} onChange={(e) => setKeyName(e.target.value)} className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm" required />
        </div>
        <button type="submit" className="inline-flex items-center gap-1.5 rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">
          <KeyRound className="h-4 w-4" /> 创建 API Key
        </button>
      </form>

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/60">
              <th className="py-2 pr-3">名称</th>
              <th className="py-2 pr-3">前缀</th>
              <th className="py-2 pr-3">状态</th>
              <th className="py-2">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[#DEE8EC]">
            {keys.map((k) => (
              <tr key={k.id}>
                <td className="py-2.5 pr-3 font-medium">{k.name}</td>
                <td className="py-2.5 pr-3 font-mono text-xs">{k.keyPrefix}...</td>
                <td className="py-2.5 pr-3 text-xs">{k.status === 'active' ? '生效中' : '已吊销'}</td>
                <td className="py-2.5 space-x-2">
                  {k.status === 'active' && (
                    <>
                      <button type="button" onClick={() => rotateKey(k.id)} className="inline-flex items-center gap-1 rounded-lg border border-[#DEE8EC] px-2.5 py-1 text-xs hover:border-[#087F8C]">
                        <RefreshCw className="h-3 w-3" /> 轮换
                      </button>
                      <button type="button" onClick={() => revokeKey(k.id)} className="inline-flex items-center gap-1 rounded-lg border border-red-200 px-2.5 py-1 text-xs text-red-700 hover:bg-red-50">
                        <Trash2 className="h-3 w-3" /> 吊销
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export function OrdersPage({ session }) {
  const [orders, setOrders] = useState([]);
  const [subs, setSubs] = useState([]);
  const [inspectOrder, setInspectOrder] = useState(null);
  const [statusMsg, setStatusMsg] = useState(null);

  async function refresh() {
    const [o, s] = await Promise.all([apiRequest('/api/billing/orders'), apiRequest('/api/billing/subscriptions')]);
    setOrders(o.data.orders ?? []);
    setSubs(s.data.subscriptions ?? []);
  }

  useEffect(() => {
    refresh().catch((err) => setStatusMsg({ tone: 'error', text: err.message }));
  }, [session.activeAccountId]);

  async function toggleAutoRenew(sub) {
    try {
      await apiRequest(`/api/billing/subscriptions/${sub.id}/auto-renew`, {
        method: 'PUT',
        csrf: session.csrf,
        body: { autoRenew: !sub.autoRenew },
      });
      await refresh();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function cancelScheduled(subId) {
    try {
      await apiRequest(`/api/billing/subscriptions/${subId}/scheduled-change`, { method: 'DELETE', csrf: session.csrf });
      await refresh();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold text-[#142B35]">订单与订阅周期</h1>
      {statusMsg && <StatusAlert message={statusMsg.text} tone={statusMsg.tone} onClose={() => setStatusMsg(null)} />}

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3">
        <h2 className="text-sm font-bold text-[#142B35]">应用订阅列表</h2>
        <div className="space-y-3">
          {subs.map((sub) => (
            <div key={sub.id} className="rounded-xl border border-[#DEE8EC] p-4 flex flex-wrap items-center justify-between gap-3">
              <div className="space-y-1">
                <div className="text-sm font-bold">{sub.tierId} · {sub.status}</div>
                <div className="text-xs text-[#142B35]/70">
                  周期：{sub.billingCycle} · 席位：{sub.seatCount} · 到期：{formatDateTime(sub.periodEnd)}
                </div>
                {sub.scheduledChange && (
                  <div className="text-xs font-medium text-[#087F8C]">
                    已预约下周期变更为：{sub.scheduledChange.targetTierId}（{sub.scheduledChange.targetSeatCount} 席）
                  </div>
                )}
              </div>
              {sub.status === 'active' && (
                <div className="flex gap-2">
                  <button type="button" onClick={() => toggleAutoRenew(sub)} className="rounded-lg border border-[#DEE8EC] px-3 py-1.5 text-xs font-medium hover:border-[#087F8C]">
                    {sub.autoRenew ? '关闭自动续费' : '开启自动续费'}
                  </button>
                  {sub.scheduledChange && (
                    <button type="button" onClick={() => cancelScheduled(sub.id)} className="rounded-lg border border-amber-300 px-3 py-1.5 text-xs text-amber-800">
                      取消预约变更
                    </button>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>

      {inspectOrder && (
        <div className="rounded-2xl border-2 border-[#087F8C] bg-white p-6 space-y-3">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-bold">不可变成交快照：{inspectOrder.orderNo}</h3>
            <button type="button" onClick={() => setInspectOrder(null)} className="text-xs underline">关闭</button>
          </div>
          <pre className="max-h-72 overflow-auto rounded-xl bg-[#F3F8FA] p-4 text-xs">
            {JSON.stringify(inspectOrder.snapshot, null, 2)}
          </pre>
        </div>
      )}

      <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 space-y-3 overflow-x-auto">
        <h2 className="text-sm font-bold text-[#142B35]">历史订单</h2>
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/60">
              <th className="py-2 pr-3">订单号</th>
              <th className="py-2 pr-3">类型</th>
              <th className="py-2 pr-3">实付</th>
              <th className="py-2 pr-3">已退/折算</th>
              <th className="py-2 pr-3">状态</th>
              <th className="py-2">快照</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[#DEE8EC]">
            {orders.map((o) => (
              <tr key={o.id}>
                <td className="py-2 pr-3 font-mono text-xs">{o.orderNo}</td>
                <td className="py-2 pr-3 text-xs">{o.orderType}</td>
                <td className="py-2 pr-3 tabular-nums">{formatMoney(o.paidMinor, o.currency)}</td>
                <td className="py-2 pr-3 tabular-nums">{formatMoney(o.refundedMinor, o.currency)}</td>
                <td className="py-2 pr-3 text-xs">{o.status}</td>
                <td className="py-2">
                  <button type="button" onClick={() => setInspectOrder(o)} className="text-xs font-medium text-[#087F8C] hover:underline">查看快照</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export function EnterprisePage({ session, publicApps, onRefreshSession }) {
  const [newName, setNewName] = useState('');
  const [members, setMembers] = useState([]);
  const [appId, setAppId] = useState(publicApps[0]?.id ?? '');
  const [seats, setSeats] = useState([]);
  const [subs, setSubs] = useState([]);
  const [inviteUrl, setInviteUrl] = useState('');
  const [statusMsg, setStatusMsg] = useState(null);

  const isEnt = session.activeAccount?.kind === 'enterprise';
  const entId = session.activeAccountId;

  useEffect(() => {
    if (!appId && publicApps[0]?.id) setAppId(publicApps[0].id);
  }, [publicApps, appId]);

  async function loadEnterprise() {
    if (!isEnt) return;
    const [mRes, subRes] = await Promise.all([
      apiRequest(`/api/accounts/${entId}/members`).catch(() => ({ data: { members: [] } })),
      apiRequest('/api/billing/subscriptions'),
    ]);
    setMembers(mRes.data.members ?? []);
    setSubs(subRes.data.subscriptions ?? []);
    if (appId) {
      const sRes = await apiRequest(`/api/accounts/${entId}/apps/${appId}/seats`).catch(() => ({ data: { seats: [] } }));
      setSeats(sRes.data.seats ?? []);
    }
  }

  useEffect(() => {
    loadEnterprise();
  }, [entId, isEnt, appId]);

  async function createEnt(e) {
    e.preventDefault();
    try {
      const res = await apiRequest('/api/accounts/enterprises', { method: 'POST', csrf: session.csrf, body: { name: newName } });
      await apiRequest('/api/session/account', { method: 'POST', csrf: session.csrf, body: { accountId: res.data.accountId } });
      setNewName('');
      await onRefreshSession();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function generateInvite() {
    try {
      const expiresAt = new Date(Date.now() + 7 * 86_400_000).toISOString();
      const res = await apiRequest(`/api/accounts/${entId}/invites`, {
        method: 'POST', csrf: session.csrf, body: { role: 'member', expiresAt },
      });
      setInviteUrl(`${window.location.origin}/invite/${res.data.token}`);
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  const activeAppSub = subs.find((s) => s.appId === appId && s.status === 'active');

  async function assignSeat(userId) {
    if (!activeAppSub) return setStatusMsg({ tone: 'error', text: '该应用尚无生效企业订阅。' });
    try {
      await apiRequest(`/api/accounts/${entId}/apps/${appId}/seats`, {
        method: 'POST', csrf: session.csrf, body: { subscriptionId: activeAppSub.id, userId },
      });
      await loadEnterprise();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  async function revokeSeat(seat) {
    try {
      await apiRequest(`/api/accounts/${entId}/apps/${appId}/seats/${seat.userId}?subscriptionId=${seat.subscriptionId}`, {
        method: 'DELETE', csrf: session.csrf,
      });
      await loadEnterprise();
    } catch (err) {
      setStatusMsg({ tone: 'error', text: err.message });
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold text-[#142B35]">企业成员与按应用席位管理</h1>
      {statusMsg && <StatusAlert message={statusMsg.text} tone={statusMsg.tone} onClose={() => setStatusMsg(null)} />}

      <form onSubmit={createEnt} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[220px]">
          <label className="mb-1 block text-xs text-[#142B35]/70">创建新企业账户</label>
          <input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="例如：星云智算科技有限公司" className="w-full rounded-xl border border-[#DEE8EC] px-3 py-2 text-sm" required />
        </div>
        <button type="submit" className="inline-flex items-center gap-1 rounded-xl bg-[#087F8C] px-4 py-2 text-sm font-semibold text-white">
          <Plus className="h-4 w-4" /> 创建并切换
        </button>
      </form>

      {isEnt && (
        <>
          <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="text-sm font-bold">一次性企业成员邀请链接</div>
                <div className="text-xs text-[#142B35]/65">受邀人登录后打开链接即可加入企业（单次有效，限本区）</div>
              </div>
              <button type="button" onClick={generateInvite} className="rounded-xl bg-[#087F8C] px-4 py-2 text-xs font-semibold text-white">生成 7 天邀请链接</button>
            </div>
            {inviteUrl && (
              <div className="rounded-xl bg-[#F3F8FA] p-3 font-mono text-xs break-all">{inviteUrl}</div>
            )}
          </div>

          <div className="rounded-2xl border border-[#DEE8EC] bg-white p-5 space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-sm font-bold">应用席位分配（{activeAppSub ? `已订 ${activeAppSub.seatCount} 席 / 已分 ${seats.length} 席` : '当前应用无订阅'}）</div>
              <select value={appId} onChange={(e) => setAppId(e.target.value)} className="rounded-xl border border-[#DEE8EC] px-3 py-1.5 text-sm">
                {publicApps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
              </select>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-[#DEE8EC] text-xs text-[#142B35]/60">
                    <th className="py-2 pr-3">成员</th>
                    <th className="py-2 pr-3">角色</th>
                    <th className="py-2 pr-3">应用席位状态</th>
                    <th className="py-2">席位操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-[#DEE8EC]">
                  {members.map((m) => {
                    const seat = seats.find((s) => s.userId === m.id);
                    return (
                      <tr key={m.id}>
                        <td className="py-2.5 pr-3 font-medium">{m.name || m.id.slice(0, 8)}</td>
                        <td className="py-2.5 pr-3 text-xs">{ROLE_LABELS[m.role] ?? m.role}</td>
                        <td className="py-2.5 pr-3 text-xs">{seat ? '已分配席位' : '未分配'}</td>
                        <td className="py-2.5">
                          {seat ? (
                            <button type="button" onClick={() => revokeSeat(seat)} className="rounded-lg border border-red-200 px-2.5 py-1 text-xs text-red-700 hover:bg-red-50">
                              收回席位
                            </button>
                          ) : (
                            <button type="button" onClick={() => assignSeat(m.id)} className="rounded-lg border border-[#087F8C] px-2.5 py-1 text-xs text-[#087F8C] hover:bg-[#087F8C]/10">
                              分配席位
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

export function SecurityPage({ session }) {
  const [apps, setApps] = useState([]);
  async function load() {
    const res = await apiRequest('/api/me/authorized-apps');
    setApps(res.data.authorizedApps ?? []);
  }
  useEffect(() => { load().catch(() => {}); }, []);
  async function revoke(appId) {
    await apiRequest(`/api/me/authorized-apps/${appId}`, { method: 'DELETE', csrf: session.csrf });
    await load();
  }
  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold text-[#142B35]">授权安全中心（我授权过的应用）</h1>
      <div className="space-y-3">
        {apps.map((a) => (
          <div key={a.appId} className="rounded-2xl border border-[#DEE8EC] bg-white p-5 flex items-center justify-between gap-4">
            <div>
              <div className="font-bold text-[#142B35]">{a.name}</div>
              <div className="text-xs text-[#142B35]/65">最近授权：{formatDateTime(a.lastAuthorizedAt)}</div>
            </div>
            <button type="button" onClick={() => revoke(a.appId)} className="rounded-xl border border-red-200 px-3.5 py-2 text-xs font-medium text-red-700 hover:bg-red-50">
              撤销授权
            </button>
          </div>
        ))}
        {apps.length === 0 && (
          <div className="rounded-2xl border border-[#DEE8EC] bg-white p-6 text-sm text-[#142B35]/65">当前暂无外部已授权应用记录。</div>
        )}
      </div>
    </div>
  );
}
