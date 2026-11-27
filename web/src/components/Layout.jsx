import React from 'react';
import {
  Building2,
  CreditCard,
  FileText,
  KeyRound,
  LayoutDashboard,
  LogOut,
  ShieldCheck,
  Sliders,
  Sparkles,
  Store,
  Wallet,
} from 'lucide-react';
import { ROLE_LABELS } from '../lib/api.js';

export function NavLink({ href, onNavigate, active, className = '', children }) {
  return (
    <a
      href={href}
      onClick={(e) => {
        if (!e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey) {
          e.preventDefault();
          onNavigate(href);
        }
      }}
      className={className}
      aria-current={active ? 'page' : undefined}
    >
      {children}
    </a>
  );
}

export function PublicHeader({ route, session, regionInfo, onNavigate }) {
  const regionLabel = regionInfo?.region === 'us' ? 'US · USD ($)' : 'CN · CNY (¥)';
  return (
    <header className="sticky top-0 z-30 border-b border-[#DEE8EC] bg-white/95 backdrop-blur">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-4 py-3 sm:px-6">
        <div className="flex items-center gap-6">
          <NavLink href="/" onNavigate={onNavigate} className="flex items-center gap-2.5">
            <img src="/assets/unihub-logo-v1.png" alt="UniHub" className="h-8 w-auto" />
          </NavLink>
          <nav className="hidden items-center gap-5 text-sm font-medium text-[#142B35]/80 sm:flex">
            <NavLink href="/" onNavigate={onNavigate} active={route === '/'} className={route === '/' ? 'text-[#087F8C]' : 'hover:text-[#087F8C]'}>
              产品概览
            </NavLink>
            <NavLink href="/docs" onNavigate={onNavigate} active={route === '/docs'} className={route === '/docs' ? 'text-[#087F8C]' : 'hover:text-[#087F8C]'}>
              开发文档
            </NavLink>
            <NavLink href="/about" onNavigate={onNavigate} active={route === '/about'} className={route === '/about' ? 'text-[#087F8C]' : 'hover:text-[#087F8C]'}>
              关于与开源
            </NavLink>
            <NavLink href="/brand" onNavigate={onNavigate} active={route === '/brand'} className={route === '/brand' ? 'text-[#087F8C]' : 'hover:text-[#087F8C]'}>
              品牌规范
            </NavLink>
          </nav>
        </div>
        <div className="flex items-center gap-3">
          <span className="hidden rounded-full border border-[#DEE8EC] bg-[#F3F8FA] px-3 py-1 text-xs font-medium text-[#142B35]/80 sm:inline-block">
            {regionLabel}
          </span>
          {session?.user ? (
            <NavLink
              href="/console"
              onNavigate={onNavigate}
              className="rounded-lg bg-[#087F8C] px-4 py-2 text-sm font-medium text-white shadow-xs transition hover:bg-[#066973]"
            >
              进入控制台
            </NavLink>
          ) : (
            <a
              href="/login"
              className="rounded-lg bg-[#087F8C] px-4 py-2 text-sm font-medium text-white shadow-xs transition hover:bg-[#066973]"
            >
              登录 UniHub
            </a>
          )}
        </div>
      </div>
    </header>
  );
}

export function PortalShell({ route, session, onNavigate, onSwitchAccount, children }) {
  const isAdminRoute = route.startsWith('/admin');
  const canAccessAdmin = Boolean(session?.isPlatformOperator || (session?.operatorApps?.length ?? 0) > 0);
  const portalItems = [
    { href: '/console', label: '概览', icon: LayoutDashboard },
    { href: '/console/store', label: '商店', icon: Store },
    { href: '/console/wallet', label: '钱包', icon: Wallet },
    { href: '/console/models', label: '模型目录', icon: Sparkles },
    { href: '/console/keys', label: 'API Key', icon: KeyRound },
    { href: '/console/orders', label: '订单与订阅', icon: CreditCard },
    { href: '/console/enterprise', label: '企业席位', icon: Building2 },
    { href: '/console/security', label: '授权安全', icon: ShieldCheck },
  ];
  const adminItems = [
    { href: '/admin/apps', label: '应用与运营授权', icon: Sliders },
    { href: '/admin/commerce', label: '商品与单卡上下架', icon: Store },
    { href: '/admin/pricing', label: '模型路由与售价', icon: Sparkles },
    ...(session?.isPlatformOperator ? [{ href: '/admin/operations', label: '对账与运维配置', icon: FileText }] : []),
  ];

  const activeAccount = session?.activeAccount;
  const roleLabel = activeAccount?.kind === 'enterprise'
    ? ROLE_LABELS[activeAccount.role] ?? '企业成员'
    : '个人账户所有者';

  return (
    <div className="min-h-screen bg-[#F3F8FA] text-[#142B35]">
      <header className="sticky top-0 z-30 border-b border-[#DEE8EC] bg-white">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <div className="flex items-center gap-4">
            <NavLink href="/" onNavigate={onNavigate} className="flex items-center gap-2">
              <img src="/assets/unihub-logo-v1.png" alt="UniHub" className="h-7 w-auto" />
            </NavLink>
            <span className="rounded-md bg-[#F3F8FA] px-2.5 py-1 text-xs font-semibold text-[#087F8C]">
              {session.region?.toUpperCase()} · {session.currency}
            </span>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2 rounded-lg border border-[#DEE8EC] bg-[#F3F8FA] px-2.5 py-1.5">
              <span className="text-xs text-[#142B35]/70">付款账户</span>
              <select
                aria-label="切换付款账户"
                value={session.activeAccountId ?? ''}
                onChange={(e) => onSwitchAccount(e.target.value)}
                className="bg-transparent text-sm font-medium text-[#142B35] focus:outline-none"
              >
                {(session.accounts ?? []).map((acc) => (
                  <option key={acc.id} value={acc.id}>
                    {acc.kind === 'enterprise' ? `企业 · ${acc.name}` : `个人 · ${acc.name}`}
                  </option>
                ))}
              </select>
              <span className="rounded bg-white px-2 py-0.5 text-[11px] font-medium text-[#087F8C] border border-[#DEE8EC]">
                {roleLabel}
              </span>
            </div>

            {canAccessAdmin && (
              <NavLink
                href={isAdminRoute ? '/console' : '/admin/commerce'}
                onNavigate={onNavigate}
                className={`rounded-lg border px-3 py-1.5 text-xs font-medium transition ${
                  isAdminRoute
                    ? 'border-[#087F8C] bg-[#087F8C]/10 text-[#087F8C]'
                    : 'border-[#DEE8EC] bg-white text-[#142B35] hover:border-[#087F8C]'
                }`}
              >
                {isAdminRoute ? '返回门户控制台' : '运营管理后台'}
              </NavLink>
            )}

            <span className="text-xs font-medium text-[#142B35]/75">
              {session.user?.name || session.user?.email}
            </span>
            <form method="post" action="/logout" className="inline">
              <input type="hidden" name="csrf" value={session.csrf ?? ''} />
              <button
                type="submit"
                className="inline-flex items-center gap-1 rounded-lg border border-[#DEE8EC] px-2.5 py-1.5 text-xs text-[#142B35]/80 hover:bg-[#F3F8FA]"
              >
                <LogOut className="h-3.5 w-3.5" />
                退出
              </button>
            </form>
          </div>
        </div>
      </header>

      <div className="mx-auto grid max-w-7xl grid-cols-1 gap-6 px-4 py-6 sm:px-6 lg:grid-cols-[220px_1fr]">
        <aside className="space-y-1">
          {(isAdminRoute ? adminItems : portalItems).map((item) => {
            const Icon = item.icon;
            const active = route === item.href;
            return (
              <NavLink
                key={item.href}
                href={item.href}
                onNavigate={onNavigate}
                active={active}
                className={`flex items-center gap-2.5 rounded-xl px-3.5 py-2.5 text-sm font-medium transition ${
                  active
                    ? 'bg-[#087F8C] text-white shadow-xs'
                    : 'text-[#142B35]/80 hover:bg-white hover:text-[#087F8C]'
                }`}
              >
                <Icon className="h-4 w-4 shrink-0" />
                <span>{item.label}</span>
              </NavLink>
            );
          })}
        </aside>

        <main className="min-w-0 space-y-6">{children}</main>
      </div>
    </div>
  );
}
