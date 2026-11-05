import { useState } from "react";
import { ArrowRight, ReceiptText } from "lucide-react";
import type { Account, Batch, Capabilities, Offer, Order, Product, Session, Subscription, Wallet } from "./types";
import type { Resource } from "./useResource";
import { activeSubscription, date, financeRole, fixed, spendable } from "./money";
import { CreditApplicability, eligibleOffers, OfferEntitlements, WalletBalances } from "./forms";
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
  initialProductId?: string;
  onSelectOffer?: (offer: Offer) => void;
  onRetry?: () => void;
  onNavigate?: (page: "memberships" | "addons" | "wallets" | "bills" | "consumption") => void;
};

function productName(products: Product[], id: string) {
  return products.find((product) => product.id === id)?.name || id;
}

function OfferCard({ offer, product, current, onBrowse }: { offer: Offer; product: string; current?: boolean; onBrowse: () => void }) {
  return (
    <article className={`billing-offer-card${current ? " current" : ""}`}>
      <div className="billing-offer-head">
        <div>
          <span className="billing-offer-period">{offer.period === "year" ? "年付方案" : "月付方案"}</span>
          <h3>{offer.name}</h3><small>{product}</small>
        </div>
        {current && <span className="billing-current-tag">当前方案</span>}
      </div>
      <strong className="billing-offer-price">¥ {fixed(offer.price)}</strong>
      <span className="billing-offer-unit">{offer.period === "year" ? "每年" : "每月"}</span>
      <p>{offer.monthly_credit === "0" ? "纯功能订阅，不含模型额度" : `每月包含 ${fixed(offer.monthly_credit, 6)} 权益额度（CREDIT）`}</p>
      <OfferEntitlements offer={offer} />
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
  initialProductId,
  onSelectOffer,
  onRetry,
}: BillingHubProps) {
  const current = (subscriptions.data || []).filter((subscription) => activeSubscription(subscription));
  const available = credits.data ? spendable(credits.data) : undefined;
  const paymentReady = session.sandbox || (!capabilities.loading && !capabilities.error && capabilities.data?.real_payments_configured === true);
  const finance = financeRole(account.role);
  const [period, setPeriod] = useState("month");
  const published = eligibleOffers(offers.data || [], account.kind, session.sandbox, initialProductId);
  const plans = published.filter(offer => offer.kind === "membership" && offer.period === period);
  const currentOfferIds = new Set(current.map((subscription) => subscription.offer.id));
  const cash = (id: string) => {
    if (wallets.loading || wallets.error) return "—";
    const amount = wallets.data?.find(wallet => wallet.product_id === id && ["CNY/6", "CNY"].includes(wallet.asset))?.amount;
    return amount === undefined ? "—" : `¥ ${fixed(amount)}`;
  };
  const navigate = (page: "memberships" | "addons" | "wallets" | "bills" | "consumption") => onNavigate?.(page);
  return (
    <div className="billing-hub">
      <div className="page-heading">
        <div>
          <h1>套餐与用量</h1>
          <p>在一个页面查看方案、额度、钱包和账单状态。</p>
        </div>
        <div className="billing-summary-actions">{onRetry && <button className="text-button" onClick={onRetry}>刷新方案与用量</button>}
        <button className="text-button refresh" onClick={() => navigate("bills")}>
          <ReceiptText size={15} /> 查看账单
        </button></div>
      </div>
      {!session.sandbox && !paymentReady && <Notice>真实支付尚未开通。当前可以查看方案与用量，支付能力启用后才能购买。</Notice>}
      <section className="billing-summary-grid" aria-label="账户用量摘要">
        <Panel title="当前方案">
          {subscriptions.loading ? <p className="loading">正在加载方案…</p> : subscriptions.error ? <Notice kind="error">{subscriptions.error}</Notice> : current.length ? current.map((subscription) => (
            <div className="billing-current-plan" key={subscription.id}>
              <div><strong>{productName(products, subscription.product_id)} · {subscription.offer.name}</strong><small>有效会员 · {subscription.offer.period === "year" ? "年付" : "月付"}</small><small>已付费至 {date(subscription.paid_through)}</small></div>
              <Status value={subscription.status} />
            </div>
          )) : <Empty title="还没有生效中的方案">选择下方会员方案查看价格与权益。</Empty>}
          <div className="billing-summary-actions"><button onClick={() => navigate("memberships")}>浏览会员方案</button><button className="secondary" onClick={() => navigate("addons")}>权益加量包</button><button className="secondary" onClick={() => navigate("wallets")}>现金充值与资金管理</button></div>
        </Panel>
        <Panel title="用量摘要">
          <div className="billing-metrics">
            <div className="billing-asset"><span>账户共享权益额度（CREDIT，非人民币）</span><strong>{credits.loading || credits.error ? "—" : fixed(available, 6)}</strong><small>当前付费账户共享池</small></div>
            {finance ? <>{[["", "统一钱包可用现金"], ["llmrouter", "托管模型现金余额"]].map(([id, label]) => <div className="billing-asset" key={id}><h3>{label}</h3><strong>{cash(id)}</strong><small>人民币 · {id ? "LLMRouter 产品钱包" : "当前付费账户"}</small></div>)}</> : <p>钱包仅财务角色可查看</p>}
          </div>
          {credits.error && <Notice kind="error">{credits.error}</Notice>}
          {finance && wallets.error && <Notice kind="error">{wallets.error}</Notice>}
          {orders.error && <Notice kind="error">{orders.error}</Notice>}
          <p className="muted">原生共享现金调用按接入规则使用统一钱包；托管模型调用使用对应产品现金钱包。请在模型消费中核对实际资金来源。</p>
          <CreditApplicability products={products} />
          {finance && <details><summary>其他钱包与待处理订单</summary>{!wallets.error && <WalletBalances wallets={wallets} products={products} />}<p>待处理订单：{orders.loading || orders.error ? "—" : orders.data?.filter(order => !["FULFILLED", "REJECTED", "CANCELED", "REFUNDED"].includes(order.fulfillment_status)).length ?? "—"}</p></details>}
          <div className="billing-summary-actions"><button onClick={() => navigate("consumption")}>查看模型消费</button><button className="secondary" onClick={() => navigate("bills")}>查看账单</button></div>
        </Panel>
      </section>
      <Panel title="选择方案" action={<button className="text-button" onClick={() => navigate("memberships")}>浏览更多方案 <ArrowRight size={14} /></button>}>
        {offers.loading ? <p className="loading">正在加载方案…</p> : offers.error ? <Notice kind="error">{offers.error}</Notice> : !published.length ? <Empty title="暂无已发布方案">方案发布后会显示价格、权益和有效期。</Empty> : (
          <div className="billing-plan-sections">
            <div className="billing-periods" role="group" aria-label="付费周期">
              <button className={period === "month" ? "" : "secondary"} aria-pressed={period === "month"} onClick={() => setPeriod("month")}>月付方案</button>
              <button className={period === "year" ? "" : "secondary"} aria-pressed={period === "year"} onClick={() => setPeriod("year")}>年付方案</button>
            </div>
            {plans.length ? <div className="billing-offer-grid">{plans.map(offer => <OfferCard key={offer.id} product={productName(products, offer.product_id)} offer={offer} current={currentOfferIds.has(offer.id)} onBrowse={() => onSelectOffer ? onSelectOffer(offer) : navigate("memberships")} />)}</div> : <p>当前周期暂无可用方案，请切换付费周期。</p>}
          </div>
        )}
      </Panel>
      <p className="billing-account-note">付费账户：{account.name} · 方案、额度和账单都归属于当前账户。</p>
    </div>
  );
}
