import { useState } from "react";
import {
  ArrowRight,
  Box,
  Crown,
  Plus,
  Wallet as WalletIcon,
} from "lucide-react";
import { accountPath, idempotencyKey, request } from "./api";
import type {
  Authorization,
  Invitation,
  Member,
  Offer,
  Operation,
  Quote,
  Recharge,
  Subscription,
  Wallet,
} from "./types";
import { activeSubscription, date, financeRole, fixed, parseCash } from "./money";
import type { Shared } from "./App";
import { OperationTable } from "./App";
import type { Resource } from "./useResource";
import {
  Detail,
  Empty,
  Modal,
  Notice,
  Panel,
  ResourceState,
  Table,
} from "./ui";
export function Catalog({
  account,
  session,
  products,
  act,
  kind,
  offers,
  subscriptions,
}: Shared & {
  kind: "membership" | "addon";
  offers: Resource<Offer[]>;
  subscriptions: Subscription[];
}) {
  const [quote, setQuote] = useState<{
    value: Quote;
    subscription: Subscription;
  }>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState("");
  const eligible = (offers.data || []).filter(
    (o) =>
      o.kind === kind &&
      o.status === "published" &&
      (o.account_kind === "both" || o.account_kind === account.kind) &&
      (!o.sandbox || session.sandbox),
  );
  const canBuy = financeRole(account.role);
  const name = (id: string) => products.find((p) => p.id === id)?.name || id;
  async function choose(offer: Offer) {
    setError("");
    const current = subscriptions.find(
      (s) =>
        s.product_id === offer.product_id &&
        activeSubscription(s),
    );
    if (current && kind === "membership") {
      setLoading(offer.id);
      try {
        const value = await request<Quote>(accountPath(account.id, "quotes"), {
          method: "POST",
          body: { subscription_id: current.id, offer_id: offer.id },
          csrf: session.csrf_token,
          key: idempotencyKey(),
        });
        setQuote({ value, subscription: current });
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setLoading("");
      }
      return;
    }
    act({
      title: kind === "addon" ? "确认购买加量包" : "确认开通会员",
      path: accountPath(account.id, "orders"),
      body: { offer_id: offer.id },
      confirm: "确认购买",
      content: (
        <>
          <OfferDetails offer={offer} name={name(offer.product_id)} />
          <Notice>
            优先使用该产品钱包；统一钱包补足须有独立授权。请先核对钱包与自动设置。
          </Notice>
          {offer.sandbox && (
            <Notice>此商品为测试商品，不代表实际商业售价。</Notice>
          )}
        </>
      ),
    });
  }
  function accept() {
    if (!quote) return;
    const { value, subscription } = quote;
    setQuote(undefined);
    act({
      title: "确认会员升级",
      path: accountPath(
        account.id,
        `quotes/${encodeURIComponent(value.id)}/accept`,
      ),
      confirm: "接受报价并升级",
      content: (
        <>
          <dl>
            <Detail label="目标方案">{value.offer.name}</Detail>
            <Detail label="本次补差">¥ {fixed(value.amount)}</Detail>
            {value.offer.monthly_credit === "0" ? (
              <>
                <Detail label="订阅权益">纯功能订阅，不含模型额度</Detail>
                <Detail label="退款规则">需人工核实权益使用情况</Detail>
              </>
            ) : (
              <>
                <Detail label="立即增加额度">{fixed(value.grant_amount, 6)}</Detail>
                <Detail label="后续每月额度">
                  {fixed(value.offer.monthly_credit, 6)}
                </Detail>
              </>
            )}
            <Detail label="当前已付期限">
              {date(subscription.paid_through)}
            </Detail>
            {value.offer.monthly_credit !== "0" && (
              <Detail label="当前额度账期">{date(value.period_start)} 起</Detail>
            )}
            <Detail label="报价截止">
              {new Date(value.expires_at).toLocaleString("zh-CN")}
            </Detail>
          </dl>
          <Notice>
            {value.offer.monthly_credit === "0"
              ? "升级保持原服务期限，不发放模型额度。过期或状态变化后须重新报价。"
              : "升级保持原账期。当前剩余额度不会重新发放。过期或状态变化后须重新报价。"}
          </Notice>
        </>
      ),
    });
  }
  return (
    <Panel title={kind === "addon" ? "可用加量包" : "会员方案"}>
      {error && <Notice kind="error">{error}</Notice>}
      {offers.loading ? (
        <p className="loading">正在加载商品…</p>
      ) : offers.error ? (
        <Notice kind="error">{offers.error}</Notice>
      ) : !eligible.length ? (
        <Empty title="暂无已发布商品">
          商品价格、权益与有效期将在正式发布后展示。
        </Empty>
      ) : (
        <div className="catalog-list">
          {eligible.map((o) => {
            const current = subscriptions.find(
              (s) =>
                s.product_id === o.product_id &&
                activeSubscription(s),
            );
            const same = current?.offer.id === o.id;
            return (
              <article className="offer-row" key={o.id}>
                <div className="offer-icon">
                  {kind === "addon" ? <Box size={25} /> : <Crown size={25} />}
                </div>
                <div className="offer-summary">
                  <div className="offer-title">
                    <h3>{o.name}</h3>
                    {o.sandbox && <span className="sandbox-tag">测试商品</span>}
                  </div>
                  <p>
                    {name(o.product_id)} ·{" "}
                    {kind === "addon"
                      ? `购买后 ${o.validity_months} 个月有效`
                      : o.period === "year"
                        ? o.monthly_credit === "0"
                          ? "年付会员 · 纯功能订阅"
                          : "年付会员 · 按月发放额度"
                        : "月付会员"}
                  </p>
                  <p>
                    {kind === "membership" && o.monthly_credit === "0" ? (
                      "纯功能订阅，不含模型额度"
                    ) : (
                      <>
                        {kind === "addon" ? "包含" : "每月包含"}{" "}
                        <strong>{fixed(o.monthly_credit, 6)}</strong> 模型额度
                      </>
                    )}
                  </p>
                  {Object.entries(o.entitlements || {})
                    .filter(([key]) => key !== "sandbox")
                    .map(([key, value]) => (
                      <small key={key}>
                        {key}：{value}
                      </small>
                    ))}
                </div>
                <div className="offer-price">
                  <strong>¥ {fixed(o.price)}</strong>
                  <small>
                    {kind === "addon"
                      ? "每份"
                      : o.period === "year"
                        ? "每年"
                        : "每月"}
                  </small>
                </div>
                <button
                  disabled={
                    !canBuy ||
                    same ||
                    Boolean(loading) ||
                    Boolean(
                      current &&
                      kind === "membership" &&
                      (o.rank <= current.offer.rank ||
                        o.period !== current.offer.period),
                    )
                  }
                  onClick={() => choose(o)}
                >
                  {loading === o.id
                    ? "正在报价…"
                    : same
                      ? "当前方案"
                      : current && kind === "membership"
                        ? "获取升级报价"
                        : "购买"}
                  <ArrowRight size={15} />
                </button>
              </article>
            );
          })}
        </div>
      )}
      {!canBuy && <Notice>请联系账户所有者或财务管理员购买。</Notice>}
      {quote && (
        <Modal title="会员升级报价" onClose={() => setQuote(undefined)}>
          <div className="modal-body">
            <div className="account-lock">
              付费账户：<strong>{account.name}</strong>
            </div>
            <dl>
              <Detail label="当前方案">{quote.subscription.offer.name}</Detail>
              <Detail label="目标方案">{quote.value.offer.name}</Detail>
              <Detail label="补差价格">¥ {fixed(quote.value.amount)}</Detail>
              {quote.value.offer.monthly_credit === "0" ? (
                <>
                  <Detail label="订阅权益">纯功能订阅，不含模型额度</Detail>
                  <Detail label="退款规则">需人工核实权益使用情况</Detail>
                </>
              ) : (
                <>
                  <Detail label="当前月增加额度">
                    {fixed(quote.value.grant_amount, 6)}
                  </Detail>
                  <Detail label="后续每月额度">
                    {fixed(quote.value.offer.monthly_credit, 6)}
                  </Detail>
                </>
              )}
              <Detail label="已付服务期限">
                {date(quote.subscription.paid_through)}
              </Detail>
              <Detail label="报价有效至">
                {new Date(quote.value.expires_at).toLocaleString("zh-CN")}
              </Detail>
            </dl>
          </div>
          <div className="modal-actions">
            <button className="secondary" onClick={() => setQuote(undefined)}>
              取消
            </button>
            <button onClick={accept}>核对并继续</button>
          </div>
        </Modal>
      )}
    </Panel>
  );
}
function OfferDetails({ offer, name }: { offer: Offer; name: string }) {
  const featureSubscription =
    offer.kind === "membership" && offer.monthly_credit === "0";
  return (
    <dl>
      <Detail label="产品 / 方案">
        {name} / {offer.name}
      </Detail>
      {(offer.refund_rule || featureSubscription) && (
        <Detail label="退款规则">
          {featureSubscription
            ? "需人工核实权益使用情况"
            : offer.refund_rule === "unused_only_v1"
              ? "仅未使用权益可按规则退款"
              : "按商品规则审核"}
        </Detail>
      )}
      <Detail label="购买价格">¥ {fixed(offer.price)}</Detail>
      <Detail label="服务期限">
        {offer.kind === "addon"
          ? `${offer.validity_months} 个月`
          : offer.period === "year"
            ? "一年"
            : "一个月"}
      </Detail>
      {featureSubscription ? (
        <Detail label="订阅权益">纯功能订阅，不含模型额度</Detail>
      ) : (
        <Detail label={offer.kind === "addon" ? "本次额度" : "每月包含额度"}>
          {fixed(offer.monthly_credit, 6)}
        </Detail>
      )}
      {offer.kind === "membership" && !featureSubscription && (
        <Detail label="额度发放">
          按月发放，每月批次独立到期；年付不提前发放全年额度。
        </Detail>
      )}
    </dl>
  );
}
export function Wallets({
  account,
  session,
  products,
  act,
  wallets,
  operations,
  refresh,
}: Shared & {
  wallets: Resource<Wallet[]>;
  operations: Resource<Operation[]>;
}) {
  const [amount, setAmount] = useState("");
  const [product, setProduct] = useState("");
  const [error, setError] = useState("");
  const [recharge, setRecharge] = useState<Recharge>();
  function prepare(kind: "recharge" | "transfer") {
    setError("");
    try {
      const value = parseCash(amount);
      if (kind === "transfer" && !product)
        throw new Error("请选择要接收划拨的产品钱包。");
      act({
        title: kind === "recharge" ? "创建测试充值单" : "确认钱包划拨",
        path: accountPath(
          account.id,
          kind === "recharge" ? "recharges" : "transfers",
        ),
        body: { product_id: product, amount: value },
        content: (
          <>
            <dl>
              <Detail label="目标钱包">
                {products.find((p) => p.id === product)?.name || "统一钱包"}
              </Detail>
              <Detail label="金额">¥ {fixed(value)}</Detail>
            </dl>
            <Notice>
              {kind === "recharge"
                ? "仅创建测试订单，尚未到账；随后须明确确认测试入账。不涉及真实支付。"
                : "从当前账户的统一钱包划拨至该产品钱包，其他账户不会自动垫付。"}
            </Notice>
          </>
        ),
        onResult:
          kind === "recharge"
            ? (data) => setRecharge(data as Recharge)
            : undefined,
      });
    } catch (e) {
      setError((e as Error).message);
    }
  }
  return (
    <>
      <Panel title="现金钱包">
        <ResourceState resource={wallets} empty="暂无钱包余额">
          {(data) => (
            <div className="wallet-list">
              {data.map((w) => (
                <div key={w.product_id} className="wallet-row">
                  <WalletIcon size={21} />
                  <div>
                    <h3>
                      {w.product_id
                        ? products.find((p) => p.id === w.product_id)?.name ||
                          w.product_id
                        : "统一钱包"}
                    </h3>
                    <small>人民币</small>
                  </div>
                  <strong>¥ {fixed(w.amount)}</strong>
                </div>
              ))}
            </div>
          )}
        </ResourceState>
      </Panel>
      <Panel title={session.sandbox ? "测试充值与资金划拨" : "资金划拨"}>
        {!session.sandbox && (
          <Notice>
            真实支付渠道尚未开通。可将已有统一钱包余额划拨至产品钱包。
          </Notice>
        )}
        <div className="inline-form">
          <label>
            目标钱包
            <select
              value={product}
              onChange={(e) => setProduct(e.target.value)}
            >
              <option value="">统一钱包</option>
              {products.map((p) => (
                <option value={p.id} key={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            金额（元）
            <input
              inputMode="decimal"
              placeholder="0.00"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
            />
          </label>
          <div className="form-buttons">
            {session.sandbox && (
              <button onClick={() => prepare("recharge")}>
                创建测试充值单
              </button>
            )}
            <button
              className="secondary"
              disabled={!product}
              onClick={() => prepare("transfer")}
            >
              从统一钱包划拨
            </button>
          </div>
        </div>
        {error && <Notice kind="error">{error}</Notice>}
      </Panel>
      {recharge && (
        <Panel title="待确认的测试充值">
          <Notice>测试订单已创建，尚不能据此认定余额到账。</Notice>
          <dl>
            <Detail label="充值单">{recharge.id}</Detail>
            <Detail label="金额">¥ {fixed(recharge.amount)}</Detail>
          </dl>
          <div className="form-buttons">
            <button
              onClick={() =>
                act({
                  title: "确认测试入账",
                  path: accountPath(
                    account.id,
                    `recharges/${encodeURIComponent(recharge.id)}/sandbox-settle`,
                  ),
                  content: (
                    <>
                      <Notice>
                        你将模拟已核验的支付通知。此操作仅用于测试，不涉及真实支付。
                      </Notice>
                      <Detail label="测试充值金额">
                        ¥ {fixed(recharge.amount)}
                      </Detail>
                      <Detail label="测试订单">{recharge.id}</Detail>
                    </>
                  ),
                  onResult: () => {
                    setRecharge(undefined);
                    refresh();
                  },
                })
              }
            >
              确认测试入账
            </button>
            <button
              className="text-button"
              onClick={() => setRecharge(undefined)}
            >
              关闭此提示
            </button>
          </div>
        </Panel>
      )}
      <OperationTable resource={operations} />
    </>
  );
}
export function Members({
  account,
  session,
  act,
  members,
  incomingInvitations,
  teamInvitations,
  canManage,
  onTeamCreated,
}: Shared & {
  members: Resource<Member[]>;
  incomingInvitations: Resource<Invitation[]>;
  teamInvitations: Resource<Invitation[]>;
  canManage: boolean;
  onTeamCreated: (accountId?: string) => void;
}) {
  const [name, setName] = useState("");
  const [user, setUser] = useState("");
  const [role, setRole] = useState("member");
  const [invitation, setInvitation] = useState("");
  const [result, setResult] = useState("");
  const [error, setError] = useState("");
  function create() {
    if (!name.trim()) return;
    act({
      title: "确认创建团队",
      path: "/v1/accounts",
      body: { name: name.trim() },
      content: (
        <p>
          团队名称：<strong>{name.trim()}</strong>
          。团队将拥有独立钱包、会员、额度与账单。
        </p>
      ),
      onResult: (data) => {
        setName("");
        onTeamCreated((data as { id: string }).id);
      },
    });
  }
  function invite() {
    if (!user.trim()) return;
    act({
      title: "确认邀请成员",
      path: accountPath(account.id, "invitations"),
      body: { user_id: user.trim(), role },
      content: (
        <p>
          邀请用户 <strong>{user}</strong> 以 <strong>{role}</strong>{" "}
          加入当前团队。邀请不会发送邮件或消息，需对方通过邀请编号自行接受。
        </p>
      ),
      onResult: (data) => {
        setResult((data as { id: string }).id);
        setUser("");
      },
    });
  }
  function acceptById(id: string) {
    setError("");
    act({
      title: "确认接受团队邀请",
      path: `/v1/invitations/${encodeURIComponent(id)}/accept`,
      content: (
        <p>
          接受邀请后，此身份将加入该邀请指定的团队。邀请编号：
          <span className="mono">{id}</span>
        </p>
      ),
      onResult: (data) => {
        setInvitation("");
        onTeamCreated((data as { id: string }).id);
      },
    });
  }
  function accept() {
    if (invitation.trim()) acceptById(invitation.trim());
  }
  function roleLabel(value: string) {
    return { owner: "所有者", admin: "管理员", billing_admin: "财务管理员", member: "成员" }[value] || value;
  }
  return (
    <>
      <Panel title="待处理的团队邀请">
        <ResourceState resource={incomingInvitations} empty="暂无待处理邀请">
          {(data) => (
            <Table headers={["团队账户", "角色", "邀请编号", "操作"]}>
              {data.map((item) => (
                <tr key={item.id}>
                  <td className="mono">{item.account_id}</td>
                  <td>{roleLabel(item.role)}</td>
                  <td className="mono selectable">{item.id}</td>
                  <td>
                    <button className="text-button" aria-label={`接受邀请 ${item.id}`} onClick={() => acceptById(item.id)}>
                      接受
                    </button>
                  </td>
                </tr>
              ))}
            </Table>
          )}
        </ResourceState>
      </Panel>
      <Panel title={account.kind === "team" ? "团队成员" : "账户成员"}>
        <ResourceState resource={members} empty="暂无成员">
          {(data) => (
            <Table
              headers={[
                "成员",
                "用户 ID",
                "角色",
                ...(canManage && account.kind === "team" ? ["操作"] : []),
              ]}
            >
              {data.map((m) => (
                <tr key={m.user_id}>
                  <td>
                    <strong>{m.name || "未设置姓名"}</strong>
                    <small>{m.email}</small>
                  </td>
                  <td className="mono">{m.user_id}</td>
                  <td>{m.role}</td>
                  {canManage && account.kind === "team" && (
                    <td>
                      {m.user_id !== session.user?.id && (
                        <div className="row-actions">
                          {account.role === "owner" && m.role !== "owner" && (
                            <label>
                              <span className="sr-only">设置{m.name || m.user_id}角色</span>
                              <select
                                aria-label={`设置${m.name || m.user_id}角色`}
                                value={m.role}
                                onChange={(event) => {
                                  const nextRole = event.target.value;
                                  if (nextRole === m.role) return;
                                  act({
                                    title: "更改成员角色",
                                    method: "PATCH",
                                    path: accountPath(account.id, `members/${encodeURIComponent(m.user_id)}`),
                                    body: { role: nextRole },
                                    content: <p>将 {m.name || m.user_id} 设为{roleLabel(nextRole)}。</p>,
                                  });
                                }}
                              >
                                <option value="member">成员</option>
                                <option value="admin">管理员</option>
                                <option value="billing_admin">财务管理员</option>
                              </select>
                            </label>
                          )}
                          {m.role !== "owner" && (account.role === "owner" || (account.role === "admin" && m.role === "member")) && (
                            <button
                              className="text-button danger"
                              onClick={() =>
                                act({
                                  title: "移除成员",
                                  method: "DELETE",
                                  path: accountPath(
                                    account.id,
                                    `members/${encodeURIComponent(m.user_id)}`,
                                  ),
                                  content: (
                                    <p>
                                      移除 {m.name || m.user_id}{" "}
                                      对此团队的访问。历史账单和资金仍归团队。
                                    </p>
                                  ),
                                })
                              }
                            >
                              移除
                            </button>
                          )}
                        </div>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </Table>
          )}
        </ResourceState>
      </Panel>
      {canManage && account.kind === "team" && (
        <Panel title="团队待处理邀请">
          <ResourceState resource={teamInvitations} empty="暂无待处理邀请">
            {(data) => (
              <Table headers={["受邀用户", "角色", "邀请编号", "操作"]}>
                {data.map((item) => (
                  <tr key={item.id}>
                    <td className="mono">{item.recipient_id}</td>
                    <td>{roleLabel(item.role)}</td>
                    <td className="mono selectable">{item.id}</td>
                    <td>
                      <button
                        className="text-button danger"
                        aria-label={`撤销邀请 ${item.id}`}
                        onClick={() =>
                          act({
                            title: "撤销团队邀请",
                            method: "DELETE",
                            path: accountPath(account.id, `invitations/${encodeURIComponent(item.id)}`),
                            content: <p>撤销发给 {item.recipient_id} 的邀请。</p>,
                          })
                        }
                      >
                        撤销
                      </button>
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </ResourceState>
        </Panel>
      )}
      {canManage && account.kind === "team" && (
        <Panel title="邀请成员">
          <div className="inline-form">
            <label>
              UniHub 用户 ID
              <input
                value={user}
                onChange={(e) => setUser(e.target.value)}
                placeholder="受邀者的用户 ID"
              />
            </label>
            <label>
              角色
              <select value={role} onChange={(e) => setRole(e.target.value)}>
                <option value="member">成员</option>
                {account.role === "owner" && (
                  <>
                    <option value="admin">管理员</option>
                    <option value="billing_admin">财务管理员</option>
                    <option value="owner">所有者（需本人接受）</option>
                  </>
                )}
              </select>
            </label>
            <button disabled={!user.trim()} onClick={invite}>
              创建邀请
            </button>
          </div>
          {result && (
            <Notice>
              邀请编号：<span className="mono selectable">{result}</span>
              。请自行提供给受邀者。
            </Notice>
          )}
        </Panel>
      )}
      <div className="two-panels">
        <Panel title="创建独立团队">
          <div className="stack-form">
            <label>
              团队名称
              <input
                maxLength={100}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="输入团队名称"
              />
            </label>
            <button onClick={create} disabled={!name.trim()}>
              <Plus size={16} />
              创建团队
            </button>
          </div>
        </Panel>
        <Panel title="接受团队邀请">
          <div className="stack-form">
            <label>
              邀请编号
              <input
                value={invitation}
                onChange={(e) => setInvitation(e.target.value)}
                placeholder="粘贴收到的邀请编号"
              />
            </label>
            <button onClick={accept} disabled={!invitation.trim()}>
              接受邀请
            </button>
            {error && <Notice kind="error">{error}</Notice>}
          </div>
        </Panel>
      </div>
    </>
  );
}
export function SettingsPage({
  account,
  products,
  act,
  settings,
}: Shared & { settings: Resource<Authorization[]> }) {
  const [product, setProduct] = useState("");
  const [purpose, setPurpose] = useState("funding");
  const [enabled, setEnabled] = useState(false);
  const [per, setPer] = useState("");
  const [total, setTotal] = useState("");
  const [until, setUntil] = useState("");
  const [error, setError] = useState("");
  const current = settings.data?.find(
    (s) => s.product_id === product && s.purpose === purpose,
  );
  function select(id: string, p: string) {
    setProduct(id);
    setPurpose(p);
    const setting = settings.data?.find(
      (s) => s.product_id === id && s.purpose === p,
    );
    setEnabled(setting?.enabled || false);
    setPer(
      setting ? fixed(setting.max_per_operation, 2).replaceAll(",", "") : "",
    );
    setTotal(setting ? fixed(setting.max_total, 2).replaceAll(",", "") : "");
    setUntil(
      setting?.expires_at && !setting.expires_at.startsWith("0001")
        ? new Date(new Date(setting.expires_at).getTime() - new Date(setting.expires_at).getTimezoneOffset() * 60000).toISOString().slice(0, 10)
        : "",
    );
  }
  function save() {
    setError("");
    try {
      if (!product) throw new Error("请选择产品。");
      const one = enabled ? parseCash(per) : current?.max_per_operation || "0";
      const all = enabled ? parseCash(total) : current?.max_total || "0";
      if (enabled && (!until || new Date(until + "T23:59:59") <= new Date()))
        throw new Error("请选择未来的授权截止日期。");
      if (enabled && BigInt(one) > BigInt(all))
        throw new Error("单次上限不能超过累计上限。");
      const expires = until
        ? new Date(until + "T23:59:59").toISOString()
        : current?.expires_at || "0001-01-01T00:00:00Z";
      act({
        title: enabled ? "确认开启资金授权" : "确认关闭资金授权",
        method: "PUT",
        path: accountPath(account.id, "settings"),
        body: {
          product_id: product,
          purpose,
          enabled,
          version: current?.version || 0,
          max_per_operation: one,
          max_total: all,
          expires_at: expires,
        },
        content: (
          <>
            <dl>
              <Detail label="产品">
                {products.find((p) => p.id === product)?.name || product}
              </Detail>
              <Detail label="授权事项">
                {purpose === "funding"
                  ? "统一钱包自动补足产品资金"
                  : "会员自动续费"}
              </Detail>
              <Detail label="操作">{enabled ? "开启" : "关闭"}</Detail>
              {enabled && (
                <>
                  <Detail label="单次上限">¥ {fixed(one)}</Detail>
                  <Detail label="累计上限">¥ {fixed(all)}</Detail>
                  <Detail label="截止日期">{date(expires)}</Detail>
                </>
              )}
            </dl>
            <Notice>
              该授权仅适用于当前账户与此产品，不包含自动购买加量包。自动续费与统一钱包补足须分别开启。
            </Notice>
          </>
        ),
      });
    } catch (e) {
      setError((e as Error).message);
    }
  }
  return (
    <>
      <Notice>
        自动授权默认关闭。开启后仅在你设置的金额与期限范围内执行。
      </Notice>
      <Panel title="现有授权">
        <ResourceState resource={settings} empty="尚未开启资金授权">
          {(data) => (
            <Table
              headers={[
                "产品",
                "授权事项",
                "状态",
                "单次 / 累计上限（元）",
                "已使用（元）",
                "截止日期",
                "操作",
              ]}
            >
              {data.map((s) => (
                <tr key={s.product_id + s.purpose}>
                  <td>
                    {products.find((p) => p.id === s.product_id)?.name ||
                      s.product_id}
                  </td>
                  <td>
                    {s.purpose === "funding" ? "统一钱包补足" : "自动续费"}
                  </td>
                  <td>{s.enabled ? "已开启" : "已关闭"}</td>
                  <td>
                    {fixed(s.max_per_operation)} / {fixed(s.max_total)}
                  </td>
                  <td>{fixed(s.used)}</td>
                  <td>{date(s.expires_at)}</td>
                  <td>
                    <button
                      className="text-button"
                      onClick={() => select(s.product_id, s.purpose)}
                    >
                      编辑
                    </button>
                  </td>
                </tr>
              ))}
            </Table>
          )}
        </ResourceState>
      </Panel>
      <Panel title="设置授权">
        <div className="settings-form">
          <label>
            产品
            <select
              value={product}
              onChange={(e) => select(e.target.value, purpose)}
            >
              <option value="">选择产品</option>
              {products.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            授权事项
            <select
              value={purpose}
              onChange={(e) => select(product, e.target.value)}
            >
              <option value="funding">统一钱包自动补足</option>
              <option value="renewal">会员自动续费</option>
            </select>
          </label>
          <label>
            单次上限（元）
            <input
              inputMode="decimal"
              value={per}
              disabled={!enabled}
              onChange={(e) => setPer(e.target.value)}
              placeholder="0.00"
            />
          </label>
          <label>
            累计上限（元）
            <input
              inputMode="decimal"
              value={total}
              disabled={!enabled}
              onChange={(e) => setTotal(e.target.value)}
              placeholder="0.00"
            />
          </label>
          <label>
            授权截止日期
            <input
              type="date"
              value={until}
              disabled={!enabled}
              onChange={(e) => setUntil(e.target.value)}
            />
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            明确开启本项授权
          </label>
        </div>
        {error && <Notice kind="error">{error}</Notice>}
        <button
          disabled={!product || settings.loading || Boolean(settings.error)}
          onClick={save}
        >
          核对并保存
        </button>
      </Panel>
    </>
  );
}
