import { ArrowRight, Database, ReceiptText, Wallet as WalletIcon } from "lucide-react";
import type { Account, Batch, Capabilities, Offer, Order, Product, Session, Subscription, Wallet } from "./types";
import type { Resource } from "./useResource";
import { activeSubscription, financeRole, fixed, spendable } from "./money";
import { CreditApplicability, WalletBalances } from "./forms";
import { Empty, Notice, Panel, Status } from "./ui";

export type BillingHubProps = {
  account: Account;
  session: Session;
  products: Product[];
  offers: Resource<Offer[]>;
  subscriptions: Resource<Subscription[]>;
  credits: Resource<Batch[]>;
  wallets: Resource<Wallet[]>;
  orders: Resource<Order[]>;
  capabilities: Resource<Capabilities>;
  onNavigate?: (page: "memberships" | "addons" | "wallets" | "bills" | "consumption") => void;
};

function productName(products: Product[], id: string) {
  return products.find((product) => product.id === id)?.name || id;
}

function OfferCard({ offer, current, onBrowse }: { offer: Offer; current?: boolean; onBrowse: () => void }) {
  return (
    <article className={`billing-offer-card${current ? " current" : ""}`}>
      <div className="billing-offer-head">
        <div>
          <span className="billing-offer-period">{offer.period === "year" ? "年付方案" : "月付方案"}</span>
          <h3>{offer.name}</h3>
        </div>
        {current && <span className="billing-current-tag">当前方案</span>}
      </div>
      <strong className="billing-offer-price">¥ {fixed(offer.price)}</strong>
      <span className="billing-offer-unit">{offer.period === "year" ? "每年" : "每月"}</span>
      <p>{offer.monthly_credit === "0" ? "纯功能订阅，不含模型额度" : `每月包含 ${fixed(offer.monthly_credit, 6)} 权益额度（CREDIT）`}</p>
      {Object.entries(offer.entitlements || {}).slice(0, 3).map(([key, value]) => <small key={key}>{key}：{value}</small>)}
      <button disabled={current} onClick={onBrowse}>
        {current ? "已在使用" : "查看方案"} {!current && <ArrowRight size={15} />}
      </button>
    </article>
  );
}

export function BillingHub({
  account,
  session,
  products,
  offers,
  subscriptions,
  credits,
  wallets,
  orders,
  capabilities,
  onNavigate,
}: BillingHubProps) {
  const current = (subscriptions.data || []).filter((subscription) => activeSubscription(subscription));
  const available = credits.data ? spendable(credits.data) : undefined;
  const paymentReady = session.sandbox || (!capabilities.loading && !capabilities.error && capabilities.data?.real_payments_configured === true);
  const finance = financeRole(account.role);
  const published = (offers.data || []).filter((offer) => offer.status === "published" && (!offer.sandbox || session.sandbox));
  const monthly = published.filter((offer) => offer.kind === "membership" && offer.period !== "year");
  const yearly = published.filter((offer) => offer.kind === "membership" && offer.period === "year");
  const currentOfferIds = new Set(current.map((subscription) => subscription.offer.id));
  const navigate = (page: "memberships" | "addons" | "wallets" | "bills" | "consumption") => onNavigate?.(page);
  return (
    <div className="billing-hub">
      <div className="page-heading">
        <div>
          <h1>套餐与用量</h1>
          <p>在一个页面查看方案、额度、钱包和账单状态。</p>
        </div>
        <button className="text-button refresh" onClick={() => navigate("bills")}>
          <ReceiptText size={15} /> 查看账单
        </button>
      </div>
      {!session.sandbox && !paymentReady && <Notice>真实支付尚未开通。当前可以查看方案与用量，支付能力启用后才能购买。</Notice>}
      <section className="billing-summary-grid" aria-label="账户用量摘要">
        <Panel title="当前方案">
          {subscriptions.loading ? <p className="loading">正在加载方案…</p> : subscriptions.error ? <Notice kind="error">{subscriptions.error}</Notice> : current.length ? current.map((subscription) => (
            <div className="billing-current-plan" key={subscription.id}>
              <div><strong>{productName(products, subscription.product_id)} · {subscription.offer.name}</strong><small>有效会员 · {subscription.offer.period === "year" ? "年付" : "月付"}</small></div>
              <Status value={subscription.status} />
            </div>
          )) : <Empty title="还没有生效中的方案"><button onClick={() => navigate("memberships")}>浏览会员方案</button></Empty>}
        </Panel>
        <Panel title="用量摘要">
          <div className="billing-metrics">
            <div><Database size={18} /><span>权益额度（CREDIT，非人民币）</span><strong>{credits.loading || credits.error ? "—" : fixed(available, 6)}</strong></div>
            {credits.error && <Notice kind="error">{credits.error}</Notice>}
            {finance ? <WalletBalances wallets={wallets} products={products} /> : <p>钱包仅财务角色可查看</p>}
            <div><ReceiptText size={18} /><span>待处理订单</span><strong>{finance && !orders.loading && !orders.error ? orders.data?.filter((order) => !["FULFILLED", "REJECTED", "CANCELED", "REFUNDED"].includes(order.fulfillment_status)).length ?? "—" : "—"}</strong></div>
          </div>
          {orders.error && <Notice kind="error">{orders.error}</Notice>}
          <CreditApplicability products={products} />
          <div className="billing-summary-actions"><button onClick={() => navigate("consumption")}>查看模型消费</button><button className="secondary" onClick={() => navigate("bills")}>查看账单</button></div>
        </Panel>
      </section>
      <Panel title="选择方案" action={<button className="text-button" onClick={() => navigate("memberships")}>浏览更多方案 <ArrowRight size={14} /></button>}>
        {offers.loading ? <p className="loading">正在加载方案…</p> : offers.error ? <Notice kind="error">{offers.error}</Notice> : !published.length ? <Empty title="暂无已发布方案">方案发布后会显示价格、权益和有效期。</Empty> : (
          <div className="billing-plan-sections">
            {!!monthly.length && <section><h3>月付方案</h3><div className="billing-offer-grid">{monthly.map((offer) => <OfferCard key={offer.id} offer={offer} current={currentOfferIds.has(offer.id)} onBrowse={() => navigate("memberships")} />)}</div></section>}
            {!!yearly.length && <section><h3>年付方案</h3><div className="billing-offer-grid">{yearly.map((offer) => <OfferCard key={offer.id} offer={offer} current={currentOfferIds.has(offer.id)} onBrowse={() => navigate("memberships")} />)}</div></section>}
          </div>
        )}
      </Panel>
      <div className="billing-next-actions">
        <button onClick={() => navigate("addons")}><Database size={18} /><span><strong>查看权益加量包</strong><small>核对适用产品与独立有效期</small></span><ArrowRight size={16} /></button>
        <button onClick={() => navigate("wallets")}><WalletIcon size={18} /><span><strong>管理账户资金</strong><small>区分统一钱包与产品钱包</small></span><ArrowRight size={16} /></button>
      </div>
      <p className="billing-account-note">付费账户：{account.name} · 方案、额度和账单都归属于当前账户。</p>
    </div>
  );
}
