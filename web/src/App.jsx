import React, { useEffect, useState } from 'react';
import { PortalShell, PublicHeader } from './components/Layout.jsx';
import { apiRequest } from './lib/api.js';
import { AboutPage, BrandPage, DocsPage, HomePage } from './pages/PublicPages.jsx';
import {
  ConsoleOverview,
  EnterprisePage,
  KeysPage,
  ModelsPage,
  OrdersPage,
  SecurityPage,
  StorePage,
  WalletPage,
} from './pages/PortalPages.jsx';
import {
  AdminAppsPage,
  AdminCommercePage,
  AdminOperationsPage,
  AdminPricingPage,
} from './pages/AdminPages.jsx';
import { HandoffPage, InviteClaimPage } from './pages/FocusPages.jsx';

export default function App() {
  const [route, setRoute] = useState(() => window.location.pathname || '/');
  const [session, setSession] = useState(null);
  const [publicApps, setPublicApps] = useState([]);
  const [regionInfo, setRegionInfo] = useState({ region: 'cn', currency: 'CNY' });
  const [loaded, setLoaded] = useState(false);

  async function refreshSession() {
    try {
      const res = await apiRequest('/api/session');
      setSession(res.data);
    } catch {
      setSession(null);
    }
  }

  useEffect(() => {
    const onPop = () => setRoute(window.location.pathname || '/');
    window.addEventListener('popstate', onPop);
    Promise.all([
      apiRequest('/api/apps/public').then((res) => {
        setPublicApps(res.data.apps ?? []);
        setRegionInfo({ region: res.data.region, currency: res.data.currency });
      }).catch(() => {}),
      refreshSession(),
    ]).finally(() => setLoaded(true));
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  function navigate(nextPath) {
    window.history.pushState({}, '', nextPath);
    setRoute(nextPath.split('?')[0]);
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  async function switchAccount(accountId) {
    if (!session) return;
    await apiRequest('/api/session/account', { method: 'POST', csrf: session.csrf, body: { accountId } });
    await refreshSession();
  }

  if (!loaded) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#F3F8FA] text-sm text-[#142B35]/70">
        正在加载 UniHub...
      </div>
    );
  }

  if (route.startsWith('/invite/')) {
    const token = route.slice('/invite/'.length);
    return <InviteClaimPage token={token} session={session} onNavigate={navigate} onRefreshSession={refreshSession} />;
  }

  const isConsoleOrAdmin = route.startsWith('/console') || route.startsWith('/admin') || route === '/handoff';
  if (isConsoleOrAdmin && !session?.user) {
    return (
      <div className="min-h-screen bg-[#F3F8FA]">
        <PublicHeader route={route} session={session} regionInfo={regionInfo} onNavigate={navigate} />
        <div className="mx-auto max-w-md px-4 py-16 text-center space-y-4">
          <h1 className="text-xl font-bold text-[#142B35]">需要登录 UniHub</h1>
          <p className="text-sm text-[#142B35]/70">请先完成统一身份认证后再访问控制台或运营管理后台。</p>
          <a href="/login" className="inline-block rounded-xl bg-[#087F8C] px-6 py-2.5 text-sm font-semibold text-white">
            前往登录 UniHub
          </a>
        </div>
      </div>
    );
  }

  if (route === '/handoff' && session?.user) {
    return <HandoffPage session={session} onNavigate={navigate} />;
  }

  if (route.startsWith('/console') && session?.user) {
    return (
      <PortalShell route={route} session={session} onNavigate={navigate} onSwitchAccount={switchAccount}>
        {route === '/console/wallet' && <WalletPage session={session} onNavigate={navigate} />}
        {route === '/console/store' && <StorePage session={session} publicApps={publicApps} onNavigate={navigate} />}
        {route === '/console/models' && <ModelsPage session={session} publicApps={publicApps} />}
        {route === '/console/keys' && <KeysPage session={session} publicApps={publicApps} />}
        {route === '/console/orders' && <OrdersPage session={session} />}
        {route === '/console/enterprise' && <EnterprisePage session={session} publicApps={publicApps} onRefreshSession={refreshSession} />}
        {route === '/console/security' && <SecurityPage session={session} />}
        {route === '/console' && <ConsoleOverview session={session} publicApps={publicApps} onNavigate={navigate} />}
      </PortalShell>
    );
  }

  if (route.startsWith('/admin') && session?.user) {
    return (
      <PortalShell route={route} session={session} onNavigate={navigate} onSwitchAccount={switchAccount}>
        {route === '/admin/apps' && <AdminAppsPage session={session} />}
        {route === '/admin/commerce' && <AdminCommercePage session={session} />}
        {route === '/admin/pricing' && <AdminPricingPage session={session} />}
        {route === '/admin/operations' && <AdminOperationsPage session={session} />}
        {route === '/admin' && <AdminCommercePage session={session} />}
      </PortalShell>
    );
  }

  return (
    <div className="min-h-screen bg-[#F3F8FA] text-[#142B35]">
      <PublicHeader route={route} session={session} regionInfo={regionInfo} onNavigate={navigate} />
      {route === '/docs' && <DocsPage />}
      {route === '/about' && <AboutPage />}
      {route === '/brand' && <BrandPage />}
      {route !== '/docs' && route !== '/about' && route !== '/brand' && (
        <HomePage publicApps={publicApps} regionInfo={regionInfo} session={session} onNavigate={navigate} />
      )}
    </div>
  );
}
