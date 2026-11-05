import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { accountPath } from "./api";
import { financeRole, fixed } from "./money";
import type { Account, CallFilter, CallPage, ConsumptionCall, Product, UsageSummary } from "./types";
import { Detail, Empty, Notice, Panel, Table } from "./ui";
import { useResource } from "./useResource";

type Props = { account: Account; products: Product[]; initialProductId?: string; version?: number };
type Query = CallFilter & { cursor?: string };
type Fields = { start: string; end: string; asset: string; model: string; product: string };
const calendarDay = (value: Date) => `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`;
function initialFields(product = ""): Fields {
  const end = new Date(), start = new Date(end);
  start.setDate(start.getDate() - 29);
  return { start: calendarDay(start), end: calendarDay(end), asset: "", model: "", product };
}
function filterFor(fields: Fields): CallFilter {
  const from = new Date(`${fields.start}T00:00:00`), to = new Date(`${fields.end}T00:00:00`);
  to.setDate(to.getDate() + 1);
  if (!fields.start || !fields.end || !Number.isFinite(from.getTime()) || !Number.isFinite(to.getTime()) || from >= to || to.getTime() - from.getTime() > 90 * 86400000) throw new Error("请选择有效日期范围，最多查询 90 天。结束日期包含当天。");
  return { from: from.toISOString(), to: to.toISOString(), asset: fields.asset, model: fields.model.trim(), origin_product_id: fields.product };
}
function paramsFor(query: Query, paged = false): string {
  const params = new URLSearchParams();
  for (const key of ["from", "to", "asset", "model", "origin_product_id", "instance_id"] as const) if (query[key]) params.set(key, query[key]!);
  if (paged) {
    params.set("limit", "50");
    if (query.cursor) params.set("cursor", query.cursor);
  }
  return params.toString();
}
function quantity(value: string | undefined, asset: string): string {
  if (value === undefined || !/^-?\d+$/.test(value)) return "—";
  if (asset.endsWith("/6")) return fixed(value, 6);
  // Legacy quota is an integer asset, not a micro-CNY or credit balance.
  return BigInt(value).toLocaleString("en-US");
}
function assetName(asset: string) {
  return {"CNY/6": "人民币", "CREDIT/6": "权益额度", "LR_QUOTA/0": "旧额度"}[asset] || asset || "资产未记录";
}
const states: Record<string, string> = { SETTLED: "已结算", RELEASED: "已释放", RESERVED: "预占中", PENDING: "处理中", REJECTED: "未完成", FAILED: "未完成", CANCELED: "已取消", MISSING_USAGE: "用量待补齐", UNKNOWN: "待核实" };
function CallState({ call }: { call: ConsumptionCall }) {
  const uncertain = !states[call.status] || call.status === "UNKNOWN";
  return <><span className={`status ${uncertain ? "warn" : call.status === "SETTLED" ? "good" : ""}`}>{states[call.status] || "待核实"}</span>{uncertain && <small>结果待核实，请勿重复支付或调用。</small>}{call.status === "MISSING_USAGE" && <small>等待用量回执，尚不能据此确认最终扣费。</small>}</>;
}
function fundingText(call: ConsumptionCall, productName: (id: string) => string): string {
  if (call.funding_mode === "prepaid_cash") {
    if (call.funding_product_id) return `产品现金钱包 · ${productName(call.funding_product_id)}`;
    if (call.source === "product_runtime") return "个人统一钱包";
    if (call.source === "managed_model") return "llmrouter 产品钱包";
    return "现金来源未记录";
  }
  if (call.funding_mode === "credits") return "权益额度（CREDIT）";
  return call.funding_mode || "资金模式未记录";
}

export function Consumption(props: Props) {
  // Account and role changes discard filters/cursors and abort the old hooks.
  return <AccountConsumption key={`${props.account.id}/${props.account.role}`} {...props} />;
}
function AccountConsumption({ account, products, initialProductId, version = 0 }: Props) {
  const confirmedProduct = products.some(product => product.id === initialProductId) ? initialProductId! : "";
  const [fields, setFields] = useState(() => initialFields(confirmedProduct));
  const [hintApplied, setHintApplied] = useState(Boolean(confirmedProduct));
  const [query, setQuery] = useState<Query>(() => filterFor(fields));
  const [history, setHistory] = useState<Query[]>([]);
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!hintApplied && confirmedProduct) {
      const nextFields = {...fields, product: confirmedProduct};
      setFields(nextFields); setQuery(filterFor(nextFields)); setHistory([]); setHintApplied(true);
    }
  }, [confirmedProduct, hintApplied, fields]);
  const calls = useResource<CallPage>(accountPath(account.id, `calls?${paramsFor(query, true)}`), version + revision);
  const summary = useResource<UsageSummary>(accountPath(account.id, `usage/summary?${paramsFor(query)}`), version + revision);
  const finance = financeRole(account.role);
  const financial = finance && calls.data?.financial_details === true;
  const productName = (id: string) => products.find(product => product.id === id)?.name || id || "未记录产品";
  function search(event: FormEvent) {
    event.preventDefault();
    try {
      const filter = filterFor(fields);
      setError(""); setHistory([]); setQuery(filter); setRevision(value => value + 1);
    } catch (e) { setError((e as Error).message); }
  }
  function next() {
    if (!calls.data?.has_more || !calls.data.next_cursor || calls.loading) return;
    setHistory(previous => [...previous, query]);
    // Keep the server-restored fixed time bounds with the signed cursor.
    setQuery({ ...query, ...calls.data.filter, cursor: calls.data.next_cursor });
  }
  function previous() {
    const prior = history.at(-1);
    if (!prior || calls.loading) return;
    setQuery(prior); setHistory(values => values.slice(0, -1));
  }
  const change = (name: keyof Fields, value: string) => {setHintApplied(true); setFields(current => ({ ...current, [name]: value }));};
  const amountTotals = summary.data?.charges_by_asset || (summary.data?.asset && summary.data.charged !== undefined ? { [summary.data.asset]: summary.data.charged } : {});
  return <div className="consumption-page">
    <Notice>{finance ? "当前账户调用；财务信息以服务端核验权限为准。" : "仅显示本人调用，金额等财务信息仅所有者和财务管理员可见。"}原付款账户保留在调用记录中，切换账户不会迁移历史消费。</Notice>
    <Panel title="查询模型消费">
      <form className="consumption-filters" onSubmit={search}>
        <label>开始日期<input type="date" required value={fields.start} onChange={event => change("start", event.target.value)} /></label>
        <label>结束日期<input type="date" required value={fields.end} onChange={event => change("end", event.target.value)} /></label>
        <label>产品<select value={fields.product} onChange={event => change("product", event.target.value)}><option value="">全部产品</option>{products.map(product => <option key={product.id} value={product.id}>{product.name}</option>)}</select></label>
        <label>资产<select value={fields.asset} onChange={event => change("asset", event.target.value)}><option value="">全部资产（分别展示）</option><option value="CNY/6">人民币 CNY/6</option><option value="CREDIT/6">权益额度 CREDIT/6</option><option value="LR_QUOTA/0">旧额度 LR_QUOTA/0</option></select></label>
        <label>模型<input maxLength={256} placeholder="模型名称（精确匹配）" value={fields.model} onChange={event => change("model", event.target.value)} /></label>
        <button type="submit">查询</button>
      </form>
      <p className="muted">按本地日历日期查询，包含结束当天，最多 90 天。分页保持同一时间范围；不同资产分别展示，不跨资产相加。</p>
      {error && <Notice kind="error">{error}</Notice>}
    </Panel>
    <Panel title="范围摘要">
      {summary.loading ? <p role="status" className="loading">正在加载摘要…</p> : summary.error ? <Notice kind="error">{summary.error}</Notice> : summary.data && <>
        <div className="consumption-totals"><span>调用 {summary.data.calls_total}</span><span>已结算 {summary.data.settled_calls}</span><span>处理中 {summary.data.pending_calls}</span><span>待核实 {summary.data.unknown_calls}</span></div>
        {finance && summary.data.financial_details && <div className="consumption-assets" aria-label="按资产列示实扣">{Object.entries(amountTotals).map(([asset, amount]) => <span key={asset}>{assetName(asset)} · {quantity(amount, asset)}<details><summary>资产标识</summary>{asset}</details></span>)}</div>}
        {finance && summary.data.financial_details && <div className="consumption-assets" aria-label="按资产列示返还">{Object.entries(summary.data.refunds_by_asset || {}).map(([asset, amount]) => <span key={asset}>返还 {assetName(asset)} · {quantity(amount, asset)}<details><summary>资产标识</summary>{asset}</details></span>)}</div>}
        {finance && summary.data.financial_details && <p className="muted">实扣为原始扣费，返还单独列示，不以净额覆盖历史实扣。</p>}
        {!summary.data.usage_complete && <p className="muted">用量回执尚不完整或含原生计量；请按每次调用记录核对，不把缺失用量视为零。</p>}
      </>}
    </Panel>
    <Panel title="调用记录" action={calls.error || summary.error ? <button className="text-button" onClick={() => setRevision(value => value + 1)}>重新查询</button> : undefined}>
      {calls.loading ? <p role="status" className="loading">正在加载调用…</p> : calls.error ? <Notice kind="error">{calls.error}{calls.error.includes("登录") && <a href="/auth/login">重新登录</a>}游标失效或条件变化时可点击查询，从第一页重新核对。</Notice> : !calls.data ? <Notice>调用结果未返回，请重新查询核对。</Notice> : !calls.data.items?.length ? <Empty title="当前条件下暂无模型消费">可调整日期、产品或资产后重新查询。</Empty> : <>
        <Table headers={["时间", "模型", "产品", ...(financial ? ["扣费"] : []), "状态"]}>
          {calls.data.items.map(call => <tr key={`${call.source}/${call.call_id}`}>
            <td>{call.created_at ? new Date(call.created_at).toLocaleString("zh-CN") : "时间未记录"}
              <details className="call-details"><summary>查看调用详情</summary><dl>
                <Detail label="调用编号"><span className="mono">{call.source_call_id || call.call_id}</span></Detail>
                {call.source_call_id && <Detail label="完整记录编号">{call.call_id}</Detail>}
                <Detail label="来源">{call.source === "product_runtime" ? "原生产品" : call.source === "managed_model" ? "托管模型" : "来源未记录"}</Detail>
                <Detail label="产品标识">{call.origin_product_id || "未记录"}</Detail>
                <Detail label="产品实例">{call.instance_id || "未记录"}</Detail>
                <Detail label="操作">{call.operation || "未记录操作"}</Detail>
                <Detail label="资产标识">{call.asset || "未记录资产"}</Detail>
                <Detail label="调用人">{call.actor_id || "未记录"}</Detail>
                <Detail label="时间来源">{call.time_source || "未记录"}</Detail>
                <Detail label="原付款账户">{call.account_id || "未记录"}</Detail>
                <Detail label="原始状态">{call.status}</Detail>
                {financial && call.financial_details && <>
                  <Detail label="预占">{quantity(call.held, call.asset)}</Detail>
                  <Detail label="原始实扣">{quantity(call.charged, call.asset)}</Detail>
                  <Detail label="返还">{quantity(call.refunded, call.asset)}</Detail>
                  <Detail label="资金来源">{fundingText(call, productName)}</Detail>
                  <Detail label="资金产品标识">{call.funding_product_id === "" ? "统一钱包（空产品标识）" : call.funding_product_id ?? "未记录"}</Detail>
                  <Detail label="价格版本">{call.price_version || "未记录"}</Detail>
                  <Detail label="定价方">{call.price_owner || "未记录"}</Detail>
                </>}
                <Detail label="用量状态">{call.usage_status || "未记录"}</Detail>
              </dl></details>
            </td>
            <td>{call.model || "未记录模型"}</td>
            <td>{productName(call.origin_product_id)}</td>
            {financial && <td className="amount">{call.financial_details ? <>
              <span>{call.charged === undefined || (call.charged === "0" && !["SETTLED", "RELEASED"].includes(call.status)) ? "扣费待核实" : quantity(call.charged, call.asset)}</span><small>{assetName(call.asset)}</small>
              <small>返还 {quantity(call.refunded, call.asset)}</small>
            </> : "—"}</td>}
            <td><CallState call={call} />{call.waiver && <small>客户费用：{call.waiver.customer_status === "RELEASED" ? "已释放" : call.waiver.customer_status === "PENDING" ? "释放处理中" : "释放待核实"}；供应商费用：待核实<span className="mono">{call.waiver.case_id}</span></small>}</td>
          </tr>)}
        </Table>
        <div className="consumption-pagination"><button className="secondary" disabled={!history.length} onClick={previous}>上一页</button><span>第 {history.length + 1} 页 · 当前范围 {calls.data.total} 条</span><button className="secondary" disabled={!calls.data.has_more || !calls.data.next_cursor} onClick={next}>下一页</button></div>
        <p className="muted">实时查询，历史记录可能更新。{calls.data.unknown_time !== "0" && `另有 ${calls.data.unknown_time} 条时间不明记录，未计入此日期范围。`}待核实、缺失回执和已释放是独立状态，不能据此重付。</p>
      </>}
    </Panel>
  </div>;
}
