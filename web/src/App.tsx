import { useEffect, useState } from "react";
import {
  ArrowRight,
  Box,
  CircleUserRound,
  CreditCard,
  FileText,
  Crown,
  Database,
  House,
  LogOut,
  RefreshCw,
  Settings,
  Users,
  Wallet as WalletIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { accountPath, errorText, idempotencyKey, request } from "./api";
import type {
  Account,
  Authorization,
  Batch,
  Member,
  Offer,
  Operation,
  Order,
  Product,
  Session,
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
export type Page =
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
  { id: "settings", label: "自动设置", icon: Settings },
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
export default function App() {
  const [version, setVersion] = useState(0);
  const session = useResource<Session>("/auth/session", version);
  const accounts = useResource<Account[]>(
    session.data?.authenticated ? "/v1/accounts" : undefined,
    version,
  );
  const [selected, setSelected] = useState(initialAccount);
  const [page, setPage] = useState<Page>("overview");
  const [busy, setBusy] = useState(false);
  const [logoutError, setLogoutError] = useState("");
  const account = selected
    ? accounts.data?.find((a) => a.id === selected)
    : accounts.data?.[0];
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
    return (
      <div className="login-page">
        <div className="login-brand">
          <img src="/assets/unihub-logo-v1.png" alt="归一 UniHub" />
          <h1>一个身份，连接你的产品。</h1>
          <p>集中管理个人与团队账户、产品会员、模型额度与资金。</p>
        </div>
        <div className="login-card">
          <h2>欢迎使用归一</h2>
          <p>登录后，继续管理你的账户。</p>
          {session.data?.sandbox && <Notice>测试环境 · 不涉及真实支付</Notice>}
          <a className="button primary" href="/auth/login">
            登录归一（UniHub） <ArrowRight size={17} />
          </a>
          <small>归一 UniHub · 统一身份与账户中心</small>
        </div>
      </div>
    );
  async function logout() {
    setBusy(true);
    setLogoutError("");
    try {
      await request("/auth/logout", {
        method: "POST",
        csrf: session.data?.csrf_token,
      });
      setSelected("");
      window.sessionStorage.removeItem("unihub.account");
      setVersion((v) => v + 1);
    } catch (error) {
      setLogoutError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a
          href="#overview"
          className="brand"
          onClick={() => setPage("overview")}
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
              onClick={() => setPage(id)}
            >
              <Icon size={19} />
              <span>{label}</span>
            </button>
          ))}
        </nav>
        <span className="sidebar-footer">归一 UniHub · 统一身份与账户中心</span>
      </aside>
      <div className="workspace">
        <header className="topbar">
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
            <span className="avatar">
              {(session.data.user?.name || "U").slice(0, 1).toUpperCase()}
            </span>
            <span>{session.data.user?.name || "我的账户"}</span>
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
          {account ? (
            <AccountConsole
              key={account.id}
              account={account}
              session={session.data}
              page={page}
              setPage={setPage}
              setBusy={setBusy}
              refreshAccounts={() => setVersion((v) => v + 1)}
            />
          ) : (
            !accounts.loading && <Empty title="暂无可访问的账户" />
          )}
        </main>
      </div>
    </div>
  );
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
  refreshAccounts: () => void;
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
          canManage={managementRole(account.role)}
          onTeamCreated={refreshAccounts}
        />
      )}{" "}
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
