import { useEffect, useState } from "react";
import {
  ArrowRight,
  BookOpen,
  Box,
  ChevronDown,
  CircleUserRound,
  CreditCard,
  FileText,
  Crown,
  Database,
  House,
  LogOut,
  RefreshCw,
  Settings,
  ShieldCheck,
  Users,
  Wallet as WalletIcon,
} from "lucide-react";
import type { ChangeEvent, FormEvent, ReactNode } from "react";
import { accountPath, errorText, idempotencyKey, request } from "./api";
import type {
  Account,
  Authorization,
  Batch,
  Invitation,
  Member,
  Offer,
  Operation,
  Order,
  Product,
  Session,
  Profile,
  Subscription,
  Wallet,
} from "./types";
import {
  activeSubscription,
  date,
  financeRole,
  fixed,
  managementRole,
  sum,
  spendable,
} from "./money";
import { useResource } from "./useResource";
import type { Resource } from "./useResource";
import {
  Detail,
  Empty,
  Modal,
  Notice,
  Panel,
  ResourceState,
  Status,
  Table,
} from "./ui";
import { Catalog, Members, SettingsPage, Wallets } from "./forms";
import {ProductApplications,ProductUserManagement} from "./ProductUsers";
export type Page =
 | "applications"
 | "user-management"
  | "overview"
  | "memberships"
  | "credits"
  | "addons"
  | "wallets"
  | "bills"
  | "members"
  | "settings";
export type ActionSpec = {
  title: string;
  content: ReactNode;
  path: string;
  body?: unknown;
  method?: string;
  onResult?: (data: unknown) => void;
  confirm?: string;
};
export type Shared = {
  account: Account;
  session: Session;
  products: Product[];
  refresh: () => void;
  act: (spec: ActionSpec) => void;
};
const navigation = [
  { id: "overview", label: "账户概览", icon: House },
  { id: "memberships", label: "产品会员", icon: Crown },
  { id: "credits", label: "模型额度", icon: Database },
  { id: "addons", label: "加量包", icon: Box },
  { id: "wallets", label: "钱包与充值", icon: WalletIcon },
  { id: "bills", label: "账单", icon: CreditCard },
  { id: "members", label: "团队成员", icon: Users },
  { id: "settings", label: "支付自动化", icon: Settings },
] as const;
const roles: Record<string, string> = {
  owner: "所有者",
  admin: "管理员",
  billing_admin: "财务管理员",
  member: "成员",
};
// Only a public selection hint is retained. Every read/write still requires
// server-verified membership; an invalid hint must never choose another payer.
function initialAccount() {
  const hint = new URLSearchParams(window.location.search).get("account");
  if (hint && hint.length <= 128) {
    window.sessionStorage.setItem("unihub.account", hint);
    return hint;
  }
  return window.sessionStorage.getItem("unihub.account") || "";
}
// This public destination hint contains no identity or credentials. Authentication
// and the live user-management permission check still guard the resulting page.
function initialPage(): Page {
  const view = new URLSearchParams(window.location.search).get("view");
  if (view === "user-management") {
    window.sessionStorage.setItem("unihub.return-view", view);
  }
  return window.sessionStorage.getItem("unihub.return-view") === "user-management"
    ? "user-management" : "overview";
}
export default function App() {
  const [version, setVersion] = useState(0);
  const session = useResource<Session>("/auth/session", version);
  const profile = useResource<Profile>(session.data?.authenticated ? "/v1/me/profile" : undefined, version);
  const [profileView, setProfileView] = useState<Profile>();
  const accounts = useResource<Account[]>(
    session.data?.authenticated ? "/v1/accounts" : undefined,
    version,
  );
  const [selected, setSelected] = useState(initialAccount);
  const [page, setPage] = useState<Page>(initialPage);
  useEffect(() => {
    if (session.data?.authenticated) window.sessionStorage.removeItem("unihub.return-view");
  }, [session.data?.authenticated]);
  const [profileOpen, setProfileOpen] = useState(false);
 const userManagement=useResource<{enabled:boolean;can_manage:boolean}>(session.data?.authenticated?"/v1/user-management":undefined,version);
  const [portalOpen, setPortalOpen] = useState(() => ["/guide", "/products"].includes(window.location.pathname));
  const [busy, setBusy] = useState(false);
  const [logoutError, setLogoutError] = useState("");
  const account = selected
    ? accounts.data?.find((a) => a.id === selected)
    : accounts.data?.[0];
  useEffect(() => {
    if (profile.data) setProfileView(profile.data);
    else if (!session.data?.authenticated) setProfileView(undefined);
  }, [profile.data, session.data?.authenticated]);
  useEffect(() => {
    if (accounts.data?.length && !selected) setSelected(accounts.data[0].id);
    if (selected && accounts.data?.some((item) => item.id === selected))
      window.sessionStorage.setItem("unihub.account", selected);
  }, [accounts.data, selected]);
  if (session.loading)
    return (
      <div className="auth-page">
        <img src="/assets/unihub-logo-v1.png" alt="归一 UniHub" />
        <p role="status">正在连接你的账户…</p>
      </div>
    );
  if (session.error)
    return (
      <div className="auth-page">
        <img src="/assets/unihub-logo-v1.png" alt="归一 UniHub" />
        <Notice kind="error">{session.error}</Notice>
        <button onClick={() => setVersion((v) => v + 1)}>重新连接</button>
      </div>
    );
  if (!session.data?.authenticated)
    return <ProductPortal sandbox={session.data?.sandbox} />;
  async function logout() {
    setBusy(true);
    setLogoutError("");
    try {
      await request("/auth/logout", {
        method: "POST",
        csrf: session.data?.csrf_token,
      });
      setSelected("");
      setProfileView(undefined);
      window.sessionStorage.removeItem("unihub.account");
      setVersion((v) => v + 1);
    } catch (error) {
      setLogoutError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function openPortal(view: PortalView) {
    const path = view === "home" ? "/" : `/${view}`;
    window.history.pushState({}, "", path);
    setProfileOpen(false);
    setPortalOpen(true);
  }
  if (portalOpen)
    return (
      <ProductPortal
        sandbox={session.data.sandbox}
        authenticated
        user={session.data.user}
        profile={profileView || profile.data}
        onOpenConsole={() => setPortalOpen(false)}
      />
    );
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a
          href="/"
          className="brand"
          onClick={(event) => {
            event.preventDefault();
            setProfileOpen(false);
            setPortalOpen(true);
          }}
        >
          <img src="/assets/unihub-logo-v1.png" alt="归一 UniHub" />
          <span>归一 · 统一身份与账户中心</span>
        </a>
        <nav aria-label="主导航">
          {navigation.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              className={page === id ? "active" : ""}
              aria-current={page === id ? "page" : undefined}
              onClick={() => {
                setProfileOpen(false);
                setPage(id);
              }}
            >
              <Icon size={19} />
              <span>{label}</span>
            </button>
          ))}
          <button className={page==="applications"?"active":""} onClick={()=>{setProfileOpen(false);setPage("applications")}}><Box size={19}/><span>我的应用</span></button>
          {userManagement.data?.can_manage && <button className={page==="user-management"?"active":""} onClick={()=>{setProfileOpen(false);setPage("user-management")}}><Users size={19}/><span>用户管理</span></button>}
        </nav>
        <span className="sidebar-footer">归一 UniHub · 统一身份与账户中心</span>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <nav className="console-topnav" aria-label="产品站导航">
            <button onClick={() => openPortal("home")}><House size={17} /><span>首页</span></button>
            <button onClick={() => openPortal("guide")}><BookOpen size={17} /><span>使用指南</span></button>
            <button onClick={() => openPortal("products")}><Box size={17} /><span>产品矩阵</span></button>
            <button onClick={() => setPage("overview")}><CircleUserRound size={17} /><span>控制台</span></button>
          </nav>
          <div className="account-control">
            <CircleUserRound size={18} />
            <label className="sr-only" htmlFor="account">
              当前付费账户
            </label>
            <select
              id="account"
              value={account?.id || selected || ""}
              onChange={(e) => {
                setSelected(e.target.value);
                const location = new URL(window.location.href);
                location.searchParams.set("account", e.target.value);
                window.history.replaceState(null, "", location);
              }}
              disabled={busy || accounts.loading}
            >
              {!account && <option value={selected}>{accounts.loading ? "正在加载账户…" : "请选择可访问的账户"}</option>}
              {accounts.data?.map((a) => (
                <option value={a.id} key={a.id}>
                  {a.name} · {a.kind === "team" ? "团队" : "个人"}
                </option>
              ))}
            </select>
            {account && (
              <span className="role-label">
                {roles[account.role] || account.role}
              </span>
            )}
          </div>
          <div className="user-menu">
            {(profileView || profile.data)?.avatar_url ? <img className="avatar avatar-image" src={`${(profileView || profile.data)!.avatar_url}?version=${encodeURIComponent((profileView || profile.data)!.version)}`} alt={(profileView || profile.data)?.display_name || "我的账户"} /> : <span className="avatar">{((profileView || profile.data)?.display_name || session.data.user?.name || "U").slice(0, 1).toUpperCase()}</span>}
            <button className="profile-menu-button" aria-label="账户设置" onClick={() => setProfileOpen(true)}>{(profileView || profile.data)?.display_name || session.data.user?.name || "我的账户"}</button>
            <button
              className="icon-button"
              aria-label="退出登录"
              title="退出登录"
              disabled={busy}
              onClick={logout}
            >
              <LogOut size={17} />
            </button>
          </div>
        </header>
        <main>
          {session.data.sandbox && <Notice>测试环境 · 不涉及真实支付</Notice>}
          {logoutError && <Notice kind="error">{logoutError}</Notice>}
          {accounts.error && <Notice kind="error">{accounts.error}</Notice>}
          {selected && accounts.data && !account && <Notice kind="error">所选账户不可访问，请明确选择一个账户。</Notice>}
          {profileOpen ? (
            <ProfileSettings
              profile={profileView || profile.data}
              loading={profile.loading}
              error={profile.error}
              session={session.data}
              onClose={() => setProfileOpen(false)}
              onChanged={setProfileView}
            />
          ) : page==="applications" ? <ProductApplications/> : page==="user-management" ? (
            userManagement.loading ? <Notice>正在核对管理权限…</Notice>
            : userManagement.data?.can_manage ? <ProductUserManagement csrf={session.data.csrf_token}/>
            : <Panel title="用户管理"><Notice kind="error">当前账户没有 UniHub 用户管理权限。</Notice><p>个人或团队账户的所有者身份不等于平台管理员。请由已配置的 UniHub 用户管理员在用户管理中分配产品角色；LLMRouter 管理员角色不授予 UniHub 全局管理权限。</p><p>新用户通过归一注册并登录后，会自动获得产品的默认成员权限，无需在 LLMRouter 重复注册。</p><button onClick={() => setPage("applications")}>查看我的应用</button></Panel>
          ) : account ? (
            <AccountConsole
              key={account.id}
              account={account}
              session={session.data}
              page={page}
              setPage={setPage}
              setBusy={setBusy}
              refreshAccounts={(accountId) => {
                if (accountId) setSelected(accountId);
                setVersion((v) => v + 1);
              }}
            />
          ) : (
            !accounts.loading && <Empty title="暂无可访问的账户" />
          )}
        </main>
      </div>
    </div>
  );
}

type PortalView = "home" | "guide" | "products";
function portalViewFromPath(): PortalView {
  if (window.location.pathname === "/guide") return "guide";
  if (window.location.pathname === "/products") return "products";
  return "home";
}

type PortalActionProps = {
  authenticated: boolean;
  onOpenConsole?: () => void;
};
function PortalAction({ authenticated, onOpenConsole }: PortalActionProps) {
  const href = authenticated ? "/console" : "/auth/login";
  const label = authenticated ? "进入控制台" : "登录归一（UniHub）";
  return (
    <a
      className="button portal-cta"
      href={href}
      onClick={authenticated ? (event) => {
        event.preventDefault();
        window.history.pushState({}, "", "/console");
        onOpenConsole?.();
      } : undefined}
    >
      {label} <ArrowRight size={17} />
    </a>
  );
}

function ProductPortal({ sandbox, authenticated = false, user, profile, onOpenConsole }: { sandbox?: boolean; authenticated?: boolean; user?: Session["user"]; profile?: Profile; onOpenConsole?: () => void }) {
  const [view, setView] = useState<PortalView>(portalViewFromPath);
  useEffect(() => {
    const onPopState = () => setView(portalViewFromPath());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  const navigate = (next: PortalView) => {
    const path = next === "home" ? "/" : `/${next}`;
    window.history.pushState({}, "", path);
    setView(next);
  };
  return (
    <div className="portal-page">
      <header className="portal-nav">
        <a className="portal-brand" href="/" onClick={(event) => { event.preventDefault(); navigate("home"); }}>
          <img src="/assets/unihub-logo-v1.png" alt="归一 UniHub" />
          <span>归一</span>
        </a>
        <nav aria-label="产品导航">
          <a className={view === "home" ? "active" : ""} href="/" onClick={(event) => { event.preventDefault(); navigate("home"); }}>首页</a>
          <a className={view === "guide" ? "active" : ""} href="/guide" onClick={(event) => { event.preventDefault(); navigate("guide"); }}><BookOpen size={15} />使用指南</a>
          <a className={view === "products" ? "active" : ""} href="/products" onClick={(event) => { event.preventDefault(); navigate("products"); }}>产品矩阵</a>
          <a href={authenticated ? "/console" : "/auth/login"} onClick={authenticated ? (event) => { event.preventDefault(); window.history.replaceState({}, "", "/console"); onOpenConsole?.(); } : undefined}>控制台</a>
        </nav>
        {authenticated && <a className="portal-user" href="/console" onClick={(event) => { event.preventDefault(); window.history.replaceState({}, "", "/console"); onOpenConsole?.(); }}><span className="portal-user-avatar">{(profile?.display_name || user?.name || "U").slice(0, 1).toUpperCase()}</span><span>{profile?.display_name || user?.name || "我的账户"}</span><ChevronDown size={15} /></a>}
      </header>
      <main>
        {view === "home" && <PortalHome authenticated={authenticated} sandbox={sandbox} onOpenConsole={onOpenConsole} onNavigate={navigate} />}
        {view === "guide" && <PortalGuide authenticated={authenticated} onOpenConsole={onOpenConsole} onNavigate={navigate} />}
        {view === "products" && <PortalProducts authenticated={authenticated} onOpenConsole={onOpenConsole} onNavigate={navigate} />}
      </main>
      <footer className="portal-footer"><span>归一 UniHub · 统一身份与账户中心</span><span>© 2026 UniHub</span></footer>
    </div>
  );
}

type PortalPageProps = {
  authenticated: boolean;
  onOpenConsole?: () => void;
  onNavigate: (view: PortalView) => void;
};
const productDocs = [
  {
    name: "SuperWagie",
    type: "AI 工作台",
    summary: "面向写作、知识整理和自动化执行的桌面工作台。",
    sections: [
      ["适合什么", "在一个工作区里完成写作、知识检索、项目推进和自动化任务。"],
      ["常用路径", "登录产品 → 选择项目或工作区 → 配置模型与知识来源 → 创建会话或任务。"],
      ["UniHub 负责什么", "UniHub 提供统一身份、产品会员、模型额度和账户账单；模型提供商、项目、会话与客户端偏好在 SuperWagie 内完成。"],
    ],
    features: ["写作与对话工作区", "知识库和来源检索", "项目、技能与自动化任务", "模型提供商和客户端偏好"],
    steps: ["登录后选择工作区或项目", "配置模型和知识来源", "创建会话或任务并检查结果", "回到 UniHub 查看会员与额度"],
  },
  {
    name: "AgentWiki",
    type: "知识协作",
    summary: "把页面、来源、图谱和 Agent 贡献沉淀为团队共同知识。",
    sections: [
      ["适合什么", "按空间组织页面与来源，追踪证据、关系和协作过程，让人和 Agent 共享上下文。"],
      ["常用路径", "进入空间 → 浏览页面或图谱 → 查看来源与证据 → 通过 Agent 或协作功能补充知识。"],
      ["UniHub 负责什么", "UniHub 管理登录身份、团队账户和产品权益；空间、页面、来源、图谱、Agent 权限与索引在 AgentWiki 内完成。"],
    ],
    features: ["Markdown 页面与共享预览", "来源、证据和知识图谱", "语义搜索与页面关联", "Agent 接入、空间协作与版本历史"],
    steps: ["登录后进入或创建 Space", "编写页面并检查共享预览", "添加来源、成员或 Agent", "通过搜索、图谱和版本历史继续协作"],
  },
  {
    name: "llmrouter",
    type: "模型服务",
    summary: "为接入产品提供统一的模型路由、凭证、用量和额度结算能力。",
    sections: [
      ["适合什么", "统一接入模型供应商，按产品和账户记录调用、用量、费率与结算状态。"],
      ["常用路径", "由接入产品发起调用 → 选择允许的模型能力 → 记录用量 → 在 UniHub 核对额度和账单。"],
      ["UniHub 负责什么", "UniHub 管理账户归属、额度和账单；模型路由、供应商凭证、费率和调用日志由 llmrouter 服务负责。"],
    ],
    features: ["模型路由和供应商适配", "按产品与账户隔离凭证", "调用、用量和费率记录", "额度预占、结算与账单回执"],
    steps: ["由接入产品发起模型请求", "按产品权限选择模型能力", "等待调用和用量回执", "在 UniHub 核对额度与账单"],
  },
] as const;
function PortalHome({ authenticated, sandbox, onOpenConsole, onNavigate }: PortalPageProps & { sandbox?: boolean }) {
  const capabilities = [
    { icon: <CircleUserRound />, title: "一个身份", copy: "个人与团队账户由 UniHub 统一承载，登录一次即可连接你的产品。" },
    { icon: <Users />, title: "一套关系", copy: "成员、角色与付费账户保持一致，跨产品协作不再重复配置。" },
    { icon: <CreditCard />, title: "一处核对", copy: "会员、模型额度、钱包与账单集中呈现，使用和资金更清楚。" },
  ];
  const products = [
    ["SuperWagie", "AI 工作台", "在桌面端完成写作、知识与自动化工作。", "teal"],
    ["AgentWiki", "知识协作", "沉淀页面、经验与上下文，让人和 Agent 共享知识。", "blue"],
    ["llmrouter", "模型服务", "为接入产品提供统一的模型路由、额度与用量能力。", "violet"],
  ];
  return (
    <>
      <section className="portal-hero">
        <div className="portal-hero-copy">
          <h1>一个身份，<span>连接你的产品。</span></h1>
          <p>UniHub 是所有产品共用的账户入口。登录、团队、会员、额度与账单在这里统一管理，具体工作仍回到最适合它的产品里完成。</p>
          <div className="portal-hero-actions">
            <PortalAction authenticated={authenticated} onOpenConsole={onOpenConsole} />
            <a className="portal-guide-link" href="/guide" onClick={(event) => { event.preventDefault(); onNavigate("guide"); }}>先看使用指南 <ArrowRight size={15} /></a>
          </div>
          {sandbox && <Notice>测试环境 · 不涉及真实支付</Notice>}
        </div>
        <aside className="portal-welcome" aria-label="UniHub 入口">
          <img src="/assets/unihub-logo-v1.png" alt="" />
          <h2>{authenticated ? "欢迎回来" : "从这里开始"}</h2>
          <p>{authenticated ? "继续管理你的产品、账户与团队" : "登录 UniHub，开始连接你的产品"}</p>
          <PortalAction authenticated={authenticated} onOpenConsole={onOpenConsole} />
        </aside>
      </section>
      <section className="portal-section portal-concept">
        <div className="portal-section-heading"><span className="portal-kicker">核心概念</span><h2>把账户关系留在 UniHub</h2><p>产品各自保持自己的工作方式，UniHub 负责把身份与账户能力稳定地连接起来。</p></div>
        <div className="portal-capabilities">
          {capabilities.map(({ icon, title, copy }) => <article key={title}><div className="portal-capability-icon">{icon}</div><h3>{title}</h3><p>{copy}</p></article>)}
        </div>
      </section>
      <section className="portal-section portal-products-section">
        <div className="portal-section-heading"><span className="portal-kicker">产品矩阵</span><h2>从一个账户进入每个产品</h2><p>UniHub 连接产品，不替产品承担工作台本身的设置。</p></div>
        <div className="portal-product-grid">
          {products.map(([name, type, copy, tone]) => <article className={`portal-product-card ${tone}`} key={name}><div className="portal-product-mark"><Box size={20} /></div><div><span>{type}</span><h3>{name}</h3></div><p>{copy}</p><a href="/products" onClick={(event) => { event.preventDefault(); onNavigate("products"); }}>查看接入说明 <ArrowRight size={14} /></a></article>)}
        </div>
      </section>
      <section className="portal-section portal-flow">
        <div className="portal-section-heading"><span className="portal-kicker">开始使用</span><h2>从登录到使用，三步开始</h2></div>
        <div className="portal-steps"><article><span>01</span><h3>登录 UniHub</h3><p>使用统一身份登录，个人账户会自动准备好。</p></article><article><span>02</span><h3>选择付费账户</h3><p>按需切换个人或团队账户，成员和权限由账户决定。</p></article><article><span>03</span><h3>进入产品工作</h3><p>从产品矩阵选择工作入口，继续使用产品自己的工作台。</p></article></div>
      </section>
    </>
  );
}

function PortalGuide({ authenticated, onOpenConsole, onNavigate }: PortalPageProps) {
  const items = [
    ["01", "登录 UniHub", "在首页欢迎卡片中点击统一入口完成登录；登录态下同一个入口会直接显示“进入控制台”。"],
    ["02", "选择账户", "在控制台顶部选择个人账户或团队账户。账户决定成员、角色、会员、额度与账单范围。"],
    ["03", "进入产品", "从产品矩阵选择工作入口，再回到对应产品完成项目、会话和运行时工作。"],
    ["04", "核对使用", "会员、模型额度、钱包与账单在 UniHub 查看；具体用量由产品和模型服务按接入协议记录。"],
    ["05", "从控制台回到产品站", "控制台左侧的产品站导航随时提供首页、使用指南和产品矩阵入口，不需要退出登录。"],
  ];
  return <>
    <section className="portal-page-title"><span className="portal-kicker">使用指南</span><h1>从账户开始，把每一步走清楚</h1><p>参考天枢的指南结构，把 UniHub 的入口、产品功能、使用步骤和账户边界集中说明。</p><PortalAction authenticated={authenticated} onOpenConsole={onOpenConsole} /></section>
    <div className="portal-guide-layout">
      <aside className="portal-guide-sidebar" aria-label="使用指南目录">
        <strong>使用指南</strong>
        <a href="#quick-start">快速开始</a>
        <a href="#product-docs">产品功能</a>
        <a href="#account-model">账户与权限</a>
        <a href="#billing-flow">额度与账单</a>
        <a href="#boundaries">职责边界</a>
      </aside>
      <div className="portal-guide-content">
        <section className="portal-guide-block" id="quick-start"><span className="portal-kicker">快速上手</span><h2>如何使用 UniHub</h2><p>完成登录、选择账户和进入产品后，具体工作在产品工作台中完成，统一身份和账户能力回到 UniHub 核对。</p><div className="portal-guide-list">{items.map(([number, title, copy]) => <article key={number}><span>{number}</span><div><h3>{title}</h3><p>{copy}</p></div></article>)}</div></section>
        <section className="portal-guide-block" id="product-docs"><span className="portal-kicker">详细文档</span><h2>产品功能与使用方法</h2><p>每个产品解决的问题不同，先按功能选择入口，再按步骤开始使用。</p><div className="portal-doc-grid">{productDocs.map((doc) => <article className="portal-doc-card" key={doc.name}><div className="portal-doc-card-head"><div><span>{doc.type}</span><h2>{doc.name}</h2></div><BookOpen size={20} /></div><p className="portal-doc-summary">{doc.summary}</p><div className="portal-doc-sections">{doc.sections.map(([title, copy]) => <div key={title}><h3>{title}</h3><p>{copy}</p></div>)}</div><h3 className="portal-doc-subhead">核心功能</h3><ul className="portal-doc-list">{doc.features.map((feature) => <li key={feature}>{feature}</li>)}</ul><h3 className="portal-doc-subhead">开始使用</h3><ol className="portal-doc-steps">{doc.steps.map((step) => <li key={step}>{step}</li>)}</ol><a href="/products" onClick={(event) => { event.preventDefault(); onNavigate("products"); }}>查看接入状态 <ArrowRight size={14} /></a></article>)}</div></section>
        <section className="portal-guide-block" id="account-model"><span className="portal-kicker">账户与权限</span><h2>账户、成员和权限如何协作</h2><div className="portal-guide-columns"><article><h3>个人账户</h3><p>适合个人使用。登录后自动准备，会员、额度、钱包和账单归属于当前个人账户。</p></article><article><h3>团队账户</h3><p>适合协作。所有者、管理员、财务管理员和成员按角色访问团队资源，成员关系由 UniHub 维护。</p></article><article><h3>产品权限</h3><p>进入产品后，产品自己的空间、项目、知识库和运行时权限仍由产品单独判断，不会因为拥有 UniHub 账户而自动获得。</p></article></div></section>
        <section className="portal-guide-block" id="billing-flow"><span className="portal-kicker">额度与账单</span><h2>一次使用如何被记录</h2><div className="portal-flow-detail"><div><span>01</span><h3>产品发起调用</h3><p>接入产品使用当前付费账户发起会员或模型服务请求。</p></div><div><span>02</span><h3>服务记录用量</h3><p>产品和模型服务写入调用、额度、钱包或订单状态。</p></div><div><span>03</span><h3>UniHub 统一核对</h3><p>在控制台查看会员、模型额度、钱包、账单和操作记录。</p></div></div></section>
        <section className="portal-guide-block" id="boundaries"><span className="portal-kicker">职责边界</span><h2>UniHub 管账户，产品提供工作</h2><div className="portal-boundary-list"><div><b>在 UniHub 完成</b><span>登录 · 账户 · 成员 · 会员 · 额度 · 钱包 · 账单 · 产品站导航</span></div><div><b>在产品工作区完成</b><span>工作项目 · 知识库 · 页面 · 会话 · 模型设置 · 运行任务 · 产品通知</span></div></div></section>
      </div>
    </div>
    <section className="portal-guide-next"><h2>准备好开始了吗？</h2><p>先选择产品，再回到对应工作台完成实际工作。</p><a href="/products" onClick={(event) => { event.preventDefault(); onNavigate("products"); }}>查看产品矩阵 <ArrowRight size={16} /></a></section>
  </>;
}

function PortalProducts({ authenticated, onOpenConsole, onNavigate }: PortalPageProps) {
  const products = [
    ["SuperWagie", "AI 工作台", "写作、知识、自动化与桌面端工作流。", "已接入"],
    ["AgentWiki", "知识协作", "页面、来源、图谱与 Agent 共同知识。", "已接入"],
    ["llmrouter", "模型服务", "统一模型路由、额度消耗与用量记录。", "已接入"],
  ];
  return <>
    <section className="portal-page-title"><span className="portal-kicker">产品矩阵</span><h1>一个账户，连接正在使用的产品</h1><p>产品接入 UniHub 后，共享统一身份与账户能力；每个产品仍保留自己的工作台和使用方式。</p><PortalAction authenticated={authenticated} onOpenConsole={onOpenConsole} /></section>
    <section className="portal-section portal-products-detail"><div className="portal-product-grid">{products.map(([name, type, copy, status]) => <article className="portal-product-card" key={name}><div className="portal-product-card-top"><div className="portal-product-mark"><Box size={20} /></div><span className="portal-status">{status}</span></div><span>{type}</span><h2>{name}</h2><p>{copy}</p><div className="portal-product-meta"><span>统一登录</span><span>账户额度</span><span>成员权限</span></div></article>)}</div></section>
    <section className="portal-guide-next"><h2>需要更具体的使用路径？</h2><p>使用指南会告诉你每一步由 UniHub 还是产品本身负责。</p><a href="/guide" onClick={(event) => { event.preventDefault(); onNavigate("guide"); }}>打开使用指南 <ArrowRight size={16} /></a></section>
  </>;
}

function AccountConsole({
  account,
  session,
  page,
  setPage,
  setBusy,
  refreshAccounts,
}: {
  account: Account;
  session: Session;
  page: Page;
  setPage: (p: Page) => void;
  setBusy: (v: boolean) => void;
  refreshAccounts: (accountId?: string) => void;
}) {
  const [version, setVersion] = useState(0);
  const [action, setAction] = useState<ActionSpec & { key: string }>();
  const [working, setWorking] = useState(false);
  const [actionError, setActionError] = useState("");
  const [message, setMessage] = useState("");
  const finance = financeRole(account.role);
  const products = useResource<Product[]>("/v1/products");
  const offers = useResource<Offer[]>("/v1/catalog");
  const subscriptions = useResource<Subscription[]>(
    accountPath(account.id, "subscriptions"),
    version,
  );
  const credits = useResource<Batch[]>(
    accountPath(account.id, "credits"),
    version,
  );
  const wallets = useResource<Wallet[]>(
    finance ? accountPath(account.id, "wallets") : undefined,
    version,
  );
  const orders = useResource<Order[]>(
    finance ? accountPath(account.id, "orders") : undefined,
    version,
  );
  const operations = useResource<Operation[]>(
    finance ? accountPath(account.id, "operations") : undefined,
    version,
  );
  const members = useResource<Member[]>(
    page === "members" ? accountPath(account.id, "members") : undefined,
    version,
  );
  const incomingInvitations = useResource<Invitation[]>(
    page === "members" ? "/v1/invitations" : undefined,
    version,
  );
  const teamInvitations = useResource<Invitation[]>(
    page === "members" && account.kind === "team" && managementRole(account.role)
      ? accountPath(account.id, "invitations")
      : undefined,
    version,
  );
  const settings = useResource<Authorization[]>(
    finance ? accountPath(account.id, "settings") : undefined,
    version,
  );
  const refresh = () => setVersion((v) => v + 1);
  const productName = (id: string) =>
    products.data?.find((p) => p.id === id)?.name || id;
  const prepaidProducts = (products.data || []).filter((p) =>
    p.billing_profile?.usage.funding_modes.includes("prepaid_cash"),
  );
  const act = (spec: ActionSpec) => {
    setActionError("");
    setAction({ ...spec, key: idempotencyKey() });
  };
  const shared: Shared = {
    account,
    session,
    products: products.data || [],
    refresh,
    act,
  };
  const pending =
    operations.data?.some((o) =>
      ["CREATED", "SUBMITTING"].includes(o.status),
    ) ||
    orders.data?.some(
      (o) =>
        (!["FULFILLED", "REJECTED", "CANCELED", "REFUNDED"].includes(
          o.fulfillment_status,
        ) &&
          ["PAID", "PENDING"].includes(o.payment_status)) ||
        o.refund_status === "FROZEN",
    );
  useEffect(() => {
    if (!pending) return;
    const timer = setTimeout(refresh, 2500);
    return () => clearTimeout(timer);
  }, [pending, version]);
  useEffect(() => () => setBusy(false), [setBusy]);
  async function confirm() {
    if (!action || working) return;
    setWorking(true);
    setBusy(true);
    setActionError("");
    try {
      const result = await request(action.path, {
        method: action.method || "POST",
        body: action.body ?? {},
        csrf: session.csrf_token,
        key: action.key,
      });
      action.onResult?.(result);
      setAction(undefined);
      setMessage("请求已受理。请以下方记录的实际状态为准。");
      refresh();
    } catch (error) {
      // A conflict/manual-review response may follow a durable state change.
      // Always re-read without re-submitting; keep the same confirmation key.
      setActionError((error as Error).message);
      refresh();
    } finally {
      setWorking(false);
      setBusy(false);
    }
  }
  const title = navigation.find((n) => n.id === page)!.label;
  const descriptions: Record<Page, string> = {
 applications:"打开你的产品应用。", "user-management":"管理统一用户状态与产品角色。",
    overview: "查看当前账户的会员、额度与资金。",
    memberships: "管理产品会员，查看已付期限与方案权益。",
    credits: "模型额度仅用于模型服务，不是可退款现金。",
    addons: "按需补充模型额度，独立有效期以商品规则为准。",
    wallets: "统一钱包与产品钱包分开记账，资金始终归属当前账户。",
    bills: "核对会员订单、退款与钱包记账状态。",
    members: "管理当前团队的访问与角色。",
    settings: "授权须明确开启，自动续费与自动划拨分别管理。",
  };
  const subsTable = (data: Subscription[]) => (
    <Table headers={["产品", "当前方案", "有效期", "状态"]}>
      {data.map((s) => (
        <tr key={s.id}>
          <td>{productName(s.product_id)}</td>
          <td>
            <strong>{s.offer.name}</strong>
            <small>
              {s.offer.period === "year" ? "年付" : "月付"} ·{" "}
              {s.offer.monthly_credit === "0"
                ? "纯功能订阅，不含模型额度"
                : `每月 ${fixed(s.offer.monthly_credit, 6)} 额度`}
            </small>
          </td>
          <td>{date(s.paid_through)}</td>
          <td>
            <Status value={s.status.toLowerCase() === "active" && !activeSubscription(s) ? "expired" : s.status} />
          </td>
        </tr>
      ))}
    </Table>
  );
  const available = credits.data ? spendable(credits.data) : undefined;
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>{title}</h1>
          <p>{descriptions[page]}</p>
        </div>
        <button
          className="text-button refresh"
          onClick={() => {
            refresh();
            refreshAccounts();
          }}
        >
          <RefreshCw size={15} />
          刷新
        </button>
      </div>
      {(page === "overview" || page === "credits") &&
        prepaidProducts.length > 0 && (
          <Notice>
            {prepaidProducts.map((p) => p.name).join("、")} 已启用现金按量服务。
            使用入口由各产品自己的界面提供，归一（UniHub）是可选的账户管理入口。
            现金模型用量从当前付费账户的 llmrouter 产品钱包扣款，额度不足不会自动转为现金扣款。
          </Notice>
        )}
      {message && (
        <Notice>
          {message}
          <button className="text-button" onClick={() => setMessage("")}>
            收起
          </button>
        </Notice>
      )}
      {page === "overview" && (
        <>
          <section className="metrics">
            <Metric
              icon={<WalletIcon />}
              label="统一钱包"
              note={finance ? "账户可用余额（元）" : "仅财务角色可查看"}
              value={
                finance
                  ? fixed(wallets.data?.find((w) => !w.product_id)?.amount)
                  : "—"
              }
              error={wallets.error}
            />
            <Metric
              icon={<Database />}
              label="可用模型额度"
              note="剩余有效额度"
              value={credits.loading ? "—" : fixed(available, 6)}
              error={credits.error}
            />
            <Metric
              icon={<Crown />}
              label="有效会员"
              note="当前生效的产品会员"
              value={
                subscriptions.data
                  ? String(
                      subscriptions.data.filter(
                        (s) => activeSubscription(s),
                      ).length,
                    )
                  : "—"
              }
              error={subscriptions.error}
            />
          </section>
          <div className="overview-split">
            <Panel
              title="产品会员"
              action={
                <button
                  className="text-button"
                  onClick={() => setPage("memberships")}
                >
                  查看全部 <ArrowRight size={14} />
                </button>
              }
            >
              <ResourceState
                resource={subscriptions}
                empty="暂无有效会员"
                emptyIcon={<Crown size={32} />}
                emptyDescription="您尚未开通任何产品会员。"
                emptyHeaders={["产品", "当前方案", "有效期", "状态"]}
              >
                {subsTable}
              </ResourceState>
              {!subscriptions.loading &&
                !subscriptions.error &&
                !subscriptions.data?.length && (
                  <div className="empty-action">
                    <button onClick={() => setPage("memberships")}>
                      浏览会员方案
                    </button>
                  </div>
                )}
            </Panel>
            <section className="related">
              <h2>让产品围绕同一账户协作</h2>
              <p>在归一（UniHub），一个账户即可管理多产品的会员、模型额度与资金。</p>
              <button onClick={() => setPage("credits")}>
                <Database size={21} />
                查看模型额度
                <ArrowRight size={16} />
              </button>
              <button onClick={() => setPage("wallets")}>
                <WalletIcon size={21} />
                钱包与充值
                <ArrowRight size={16} />
              </button>
            </section>
          </div>
          {finance && (
            <Panel
              title="近期账单"
              action={
                <button
                  className="text-button"
                  onClick={() => setPage("bills")}
                >
                  查看全部 <ArrowRight size={14} />
                </button>
              }
            >
              <OrderTable
                resource={orders}
                productName={productName}
                limit={5}
              />
            </Panel>
          )}
        </>
      )}
      {page === "memberships" && (
        <>
          <Panel title="当前会员">
            <ResourceState resource={subscriptions} empty="当前账户暂无会员">
              {subsTable}
            </ResourceState>
          </Panel>
          <Catalog
            {...shared}
            kind="membership"
            offers={offers}
            subscriptions={subscriptions.data || []}
          />
        </>
      )}
      {page === "addons" && (
        <Catalog
          {...shared}
          kind="addon"
          offers={offers}
          subscriptions={subscriptions.data || []}
        />
      )}
      {page === "credits" && (
        <>
          <section className="metrics">
            <Metric
              icon={<Database />}
              label="可用模型额度"
              note="可用于新的模型请求"
              value={fixed(available, 6)}
            />
            <Metric
              icon={<Database />}
              label="请求预占"
              note="待调用结算的额度"
              value={
                credits.data
                  ? fixed(sum(credits.data.map((b) => b.reserved)), 6)
                  : "—"
              }
            />
            <Metric
              icon={<Box />}
              label="加量包可用额度"
              note="独立有效期"
              value={
                credits.data
                  ? fixed(
                      spendable(credits.data.filter((b) => b.kind === "addon")),
                      6,
                    )
                  : "—"
              }
            />
          </section>
          <Panel title="额度批次">
            <ResourceState resource={credits} empty="暂无额度批次">
              {(data) => <BatchTable data={data} productName={productName} />}
            </ResourceState>
          </Panel>
        </>
      )}
      {page === "wallets" &&
        (finance ? (
          <Wallets {...shared} wallets={wallets} operations={operations} />
        ) : (
          <Notice>只有所有者和财务管理员可查看钱包与充值。</Notice>
        ))}
      {page === "bills" &&
        (finance ? (
          <>
            <Panel title="会员与加量包订单">
              <OrderTable
                resource={orders}
                productName={productName}
                onRefund={(order) =>
                  act({
                    title: "确认申请退款",
                    path: accountPath(
                      account.id,
                      `orders/${encodeURIComponent(order.id)}/refund`,
                    ),
                    content: (
                      <>
                        <Notice kind="warning">
                          退款将撤回关联权益。可退款金额与处理结果由实际账务状态核定。
                        </Notice>
                        <dl>
                          <Detail label="付费账户">{account.name}</Detail>
                          <Detail label="订单">{order.id}</Detail>
                          <Detail label="退款规则">
                            {order.offer.refund_rule === "unused_only_v1"
                              ? "仅未使用权益可按规则退款"
                              : "需按商品规则审核"}
                          </Detail>
                          <Detail label="原订单金额">
                            ¥ {fixed(order.amount)}
                          </Detail>
                        </dl>
                      </>
                    ),
                  })
                }
              />
            </Panel>
            <OperationTable resource={operations} />
          </>
        ) : (
          <Notice>只有所有者和财务管理员可查看账单。</Notice>
        ))}
      {page === "members" && (
        <Members
          {...shared}
          members={members}
          incomingInvitations={incomingInvitations}
          teamInvitations={teamInvitations}
          canManage={managementRole(account.role)}
          onTeamCreated={refreshAccounts}
        />
      )}
      {page === "settings" &&
        (finance ? (
          <SettingsPage {...shared} settings={settings} />
        ) : (
          <Notice>只有所有者和财务管理员可设置资金授权。</Notice>
        ))}
      {action && (
        <Modal
          title={action.title}
          busy={working}
          onClose={() => setAction(undefined)}
        >
          <div className="modal-body">
            <div className="account-lock">
              付费账户：<strong>{account.name}</strong>
              <small>{account.id}</small>
            </div>
            {action.content}
            {actionError && (
              <Notice kind="error">
                {actionError}{" "}
                如提交结果不明确，请先关闭窗口刷新核对；在本窗口重试会沿用同一请求编号。
              </Notice>
            )}
          </div>
          <div className="modal-actions">
            <button
              className="secondary"
              onClick={() => setAction(undefined)}
              disabled={working}
            >
              取消
            </button>
            <button onClick={confirm} disabled={working}>
              {working ? "正在提交…" : action.confirm || "确认提交"}
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
function Metric({
  icon,
  label,
  note,
  value,
  error,
}: {
  icon: ReactNode;
  label: string;
  note: string;
  value: string;
  error?: string;
}) {
  return (
    <div className="metric">
      <div className="metric-icon">{icon}</div>
      <div>
        <h3>{label}</h3>
        <p>{note}</p>
        <strong className="metric-value">{value}</strong>
        {error && <small className="error-text">{error}</small>}
      </div>
    </div>
  );
}
function BatchTable({
  data,
  productName,
}: {
  data: Batch[];
  productName: (id: string) => string;
}) {
  return (
    <Table
      headers={[
        "来源产品 / 批次",
        "类型",
        "原始额度",
        "可用",
        "预占",
        "已用",
        "撤回 / 失效",
        "到期日",
      ]}
    >
      {data.map((b) => (
        <tr key={b.id}>
          <td>
            {productName(b.product_id)}
            <small className="mono">{b.id}</small>
          </td>
          <td>
            {b.kind === "addon"
              ? "加量包"
              : b.kind === "upgrade"
                ? "升级补额"
                : "套餐额度"}
            {b.frozen && <small>已冻结 · 当前可用数量不可消费</small>}
          </td>
          <td>{fixed(b.original, 6)}</td>
          <td>{fixed(b.available, 6)}</td>
          <td>{fixed(b.reserved, 6)}</td>
          <td>{fixed(b.consumed, 6)}</td>
          <td>
            {fixed(b.withdrawn, 6)} / {fixed(b.expired, 6)}
          </td>
          <td>{date(b.expires_at)}</td>
        </tr>
      ))}
    </Table>
  );
}
function OrderTable({
  resource,
  productName,
  limit,
  onRefund,
}: {
  resource: Resource<Order[]>;
  productName: (id: string) => string;
  limit?: number;
  onRefund?: (order: Order) => void;
}) {
  return (
    <ResourceState
      resource={resource}
      empty="暂无账单"
      emptyIcon={<FileText size={30} />}
      emptyDescription="当前没有账单记录。"
      emptyHeaders={[
        "类型 / 订单",
        "所属产品",
        "金额（元）",
        "支付 / 权益状态",
        "创建时间",
      ]}
    >
      {(data) => (
        <Table
          headers={[
            "类型 / 订单",
            "所属产品",
            "金额（元）",
            "支付 / 权益状态",
            "创建时间",
            ...(onRefund ? ["操作"] : []),
          ]}
        >
          {data.slice(0, limit).map((o) => (
            <tr key={o.id}>
              <td>
                {o.offer.name}
                <small>
                  {o.kind === "addon"
                    ? "加量包"
                    : o.kind === "upgrade"
                      ? "会员升级"
                      : "产品会员"}{" "}
                  · <span className="mono">{o.id}</span>
                </small>
              </td>
              <td>{productName(o.product_id)}</td>
              <td className="amount">
                ¥ {fixed(o.amount)}
                <small>
                  产品 ¥{fixed(o.product_funding)} / 统一 ¥
                  {fixed(o.unified_funding)}
                </small>
              </td>
              <td>
                <Status value={o.payment_status} />
                <small>
                  <Status value={o.fulfillment_status} />
                  {o.refund_status && <Status value={o.refund_status} />}
                </small>
                {o.refund_reason && <small>{o.refund_reason}</small>}
              </td>
              <td>{date(o.created_at)}</td>
              {onRefund && (
                <td>
                  {o.offer.refund_rule ? (
                    <button
                      className="text-button"
                      disabled={
                        Boolean(o.refund_status) ||
                        o.payment_status !== "PAID" ||
                        o.fulfillment_status !== "FULFILLED"
                      }
                      onClick={() => onRefund(o)}
                    >
                      申请退款
                    </button>
                  ) : (
                    <span className="muted">未提供退款规则</span>
                  )}
                </td>
              )}
            </tr>
          ))}
        </Table>
      )}
    </ResourceState>
  );
}
export function OperationTable({
  resource,
}: {
  resource: Resource<Operation[]>;
}) {
  return (
    <Panel title="钱包记账操作">
      <ResourceState resource={resource} empty="暂无钱包操作">
        {(data) => (
          <Table headers={["操作编号", "类型", "状态", "创建时间"]}>
            {data.map((o) => (
              <tr key={o.id}>
                <td className="mono">{o.id}</td>
                <td>
                  {o.kind === "recharge"
                    ? "充值"
                    : o.kind === "transfer"
                      ? "钱包划拨"
                      : "钱包操作"}
                </td>
                <td>
                  <Status value={o.status} />
                  {o.status === "UNKNOWN" && (
                    <small>结果待核实，请勿重复操作</small>
                  )}
                  {o.error_code && <small>{errorText(o.error_code)}</small>}
                </td>
                <td>{date(o.created_at)}</td>
              </tr>
            ))}
          </Table>
        )}
      </ResourceState>
    </Panel>
  );
}

function ProfileSettings({
  profile,
  loading,
  error,
  session,
  onClose,
  onChanged,
}: {
  profile?: Profile;
  loading: boolean;
  error?: string;
  session: Session;
  onClose: () => void;
  onChanged: (profile: Profile) => void;
}) {
  const [current, setCurrent] = useState(profile);
  const [displayName, setDisplayName] = useState(profile?.display_name || "");
  const [savingProfile, setSavingProfile] = useState(false);
  const [profileMessage, setProfileMessage] = useState("");
  const [profileError, setProfileError] = useState("");
  const [passwords, setPasswords] = useState({ current: "", next: "", confirm: "" });
  const [savingPassword, setSavingPassword] = useState(false);
  const [passwordMessage, setPasswordMessage] = useState("");
  const [passwordError, setPasswordError] = useState("");

  useEffect(() => {
    setCurrent(profile);
    setDisplayName(profile?.display_name || "");
  }, [profile]);

  async function saveProfile() {
    setProfileError("");
    setProfileMessage("");
    setSavingProfile(true);
    try {
      const next = await request<Profile>("/v1/me/profile", {
        method: "PATCH",
        csrf: session.csrf_token,
        body: { display_name: displayName.trim() },
      });
      setCurrent(next);
      setProfileMessage("个人资料已保存。");
      onChanged(next);
    } catch (e) {
      setProfileError((e as Error).message);
    } finally {
      setSavingProfile(false);
    }
  }

  async function uploadAvatar(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    if (!(file.type === "image/png" || file.type === "image/jpeg") || file.size > 512 * 1024) {
      setProfileError("头像必须是 PNG 或 JPEG，且不超过 512KB。");
      return;
    }
    setProfileError("");
    setProfileMessage("");
    setSavingProfile(true);
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      let binary = "";
      for (let i = 0; i < bytes.length; i += 0x8000) {
        binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
      }
      const next = await request<Profile>("/v1/me/avatar", {
        method: "PUT",
        csrf: session.csrf_token,
        body: { content_type: file.type, data_base64: btoa(binary) },
      });
      setCurrent(next);
      setProfileMessage("头像已更新。");
      onChanged(next);
    } catch (e) {
      setProfileError((e as Error).message);
    } finally {
      setSavingProfile(false);
    }
  }

  async function changePassword(event: FormEvent) {
    event.preventDefault();
    setPasswordError("");
    setPasswordMessage("");
    if (passwords.next !== passwords.confirm) {
      setPasswordError("两次输入的新密码不一致。");
      return;
    }
    setSavingPassword(true);
    try {
      await request("/v1/me/password", {
        method: "POST",
        csrf: session.csrf_token,
        body: { current_password: passwords.current, new_password: passwords.next },
      });
      setPasswords({ current: "", next: "", confirm: "" });
      setPasswordMessage("密码已更新，请重新登录其他设备。");
    } catch (e) {
      setPasswordError((e as Error).message);
    } finally {
      setSavingPassword(false);
    }
  }

  const productCredentials = current?.credential_source === "product";
  const avatar = current?.avatar_url ? `${current.avatar_url}?version=${encodeURIComponent(current.version)}` : "";
  return (
    <section className="profile-settings">
      <div className="page-heading">
        <div><h1>账户设置</h1><p>管理个人资料和登录安全，统一应用到所有产品。</p></div>
        <button className="text-button" onClick={onClose}>返回账户概览</button>
      </div>
      {loading && <div className="loading" role="status">正在加载个人资料…</div>}
      {error && <Notice kind="error">{error}</Notice>}
      {!loading && !error && (
        <div className="profile-settings-grid">
          <Panel title="我的资料">
            <div className="profile-hero">
              {avatar ? <img className="profile-avatar" src={avatar} alt={current?.display_name || "我的账户"} /> : <span className="profile-avatar profile-avatar-fallback">{(current?.display_name || "U").slice(0, 1).toUpperCase()}</span>}
              {!productCredentials && <label className="avatar-upload">更换头像<input type="file" accept="image/png,image/jpeg" onChange={uploadAvatar} disabled={savingProfile} /></label>}
            </div>
            <div className="stack-form">
              <label>显示名称<input value={displayName} disabled={productCredentials} maxLength={80} onChange={(event) => setDisplayName(event.target.value)} /></label>
              <dl>
                <Detail label="登录邮箱">{current?.email || "—"}</Detail>
                <Detail label="用户名">{current?.username || "—"}</Detail>
              </dl>
              {profileError && <Notice kind="error">{profileError}</Notice>}
              {profileMessage && <Notice>{profileMessage}</Notice>}
              {!productCredentials && <button onClick={saveProfile} disabled={savingProfile || !displayName.trim()}>保存个人资料</button>}
            </div>
          </Panel>
          <Panel title="登录与安全">
            <div className="security-summary"><ShieldCheck size={20} /><div><strong>UniHub 统一身份</strong><p>{productCredentials ? "密码、二次验证及通行密钥由原产品管理，继续使用原有登录方式。" : "认证由 Casdoor 管理，资料变更会同步到所有产品。"}</p></div></div>
            {productCredentials ? <div className="stack-form">{current?.credential_management?.map((manager) => manager.url ? <a key={`${manager.product_id}/${manager.instance_id}`} href={manager.url}>前往 {manager.product_id} 管理密码与二次验证</a> : <p key={`${manager.product_id}/${manager.instance_id}`}>请在 {manager.product_id} 管理登录安全。</p>)}</div> : <form className="stack-form" onSubmit={changePassword}>
              <h3>修改密码</h3>
              <label>当前密码<input type="password" autoComplete="current-password" value={passwords.current} onChange={(event) => setPasswords({ ...passwords, current: event.target.value })} /></label>
              <label>新密码<input type="password" autoComplete="new-password" value={passwords.next} onChange={(event) => setPasswords({ ...passwords, next: event.target.value })} /></label>
              <label>确认新密码<input type="password" autoComplete="new-password" value={passwords.confirm} onChange={(event) => setPasswords({ ...passwords, confirm: event.target.value })} /></label>
              {passwordError && <Notice kind="error">{passwordError}</Notice>}
              {passwordMessage && <Notice>{passwordMessage}</Notice>}
              <button type="submit" disabled={savingPassword || !passwords.current || !passwords.next || !passwords.confirm}>更新密码</button>
            </form>}
          </Panel>
        </div>
      )}
    </section>
  );
}
