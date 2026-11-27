import React, { useState } from 'react';
import { apiRequest } from '../lib/api.js';
import { NavLink } from '../components/Layout.jsx';

export function HandoffPage({ session, onNavigate }) {
  const params = new URLSearchParams(window.location.search);
  const action = params.get('action') || 'wallet';
  const returnUrl = params.get('returnUrl');

  return (
    <div className="mx-auto max-w-xl px-4 py-12">
      <div className="rounded-3xl border border-[#DEE8EC] bg-white p-8 space-y-5 shadow-xs">
        <img src="/assets/unihub-logo-v1.png" alt="UniHub" className="h-7 w-auto" />
        <h1 className="text-xl font-bold text-[#142B35]">外部应用业务聚焦通道</h1>
        <p className="text-sm text-[#142B35]/75">
          当前付款账户：<span className="font-semibold">{session.activeAccount?.name}</span> · 请求业务：<span className="font-mono">{action}</span>
        </p>
        <div className="flex flex-wrap gap-3">
          <NavLink
            href={action === 'keys' ? '/console/keys' : action === 'store' ? '/console/store' : '/console/wallet'}
            onNavigate={onNavigate}
            className="rounded-xl bg-[#087F8C] px-4 py-2.5 text-sm font-semibold text-white"
          >
            前往对应处理页
          </NavLink>
          {returnUrl && /^https?:\/\//i.test(returnUrl) && (
            <a href={returnUrl} className="rounded-xl border border-[#DEE8EC] px-4 py-2.5 text-sm text-[#142B35]">
              返回来源应用
            </a>
          )}
        </div>
      </div>
    </div>
  );
}

export function InviteClaimPage({ token, session, onNavigate, onRefreshSession }) {
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');

  async function claim() {
    try {
      const res = await apiRequest(`/api/invites/${encodeURIComponent(token)}/claim`, {
        method: 'POST', csrf: session.csrf, body: {},
      });
      await onRefreshSession();
      setStatus(`已成功加入企业（身份：${res.data.role}），请切换到该企业账户并联系管理员分配应用席位。`);
    } catch (err) {
      setError(err.message);
    }
  }

  return (
    <div className="mx-auto max-w-lg px-4 py-12">
      <div className="rounded-3xl border border-[#DEE8EC] bg-white p-8 space-y-5 shadow-xs">
        <img src="/assets/unihub-logo-v1.png" alt="UniHub" className="h-7 w-auto" />
        <h1 className="text-xl font-bold text-[#142B35]">企业邀请认领</h1>
        {!session?.user ? (
          <div className="space-y-4">
            <p className="text-sm text-[#142B35]/75">请先登录您的 UniHub 个人身份后完成企业邀请认领。</p>
            <a href="/login" className="inline-block rounded-xl bg-[#087F8C] px-5 py-2.5 text-sm font-semibold text-white">登录 UniHub</a>
          </div>
        ) : (
          <div className="space-y-4">
            {status && <div className="rounded-xl bg-[#35C4A3]/15 p-3 text-sm">{status}</div>}
            {error && <div className="rounded-xl bg-red-50 p-3 text-sm text-red-700">{error}</div>}
            <div className="flex gap-3">
              <button type="button" onClick={claim} className="rounded-xl bg-[#087F8C] px-5 py-2.5 text-sm font-semibold text-white">
                确认加入企业
              </button>
              <NavLink href="/console/enterprise" onNavigate={onNavigate} className="rounded-xl border border-[#DEE8EC] px-4 py-2.5 text-sm">
                进入控制台
              </NavLink>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
