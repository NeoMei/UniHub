export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
  ) {
    super(errorText(code, status));
  }
}
export function errorText(code: string, status = 0): string {
  const texts: Record<string, string> = {
    operation_pending: "当前账户有待核实操作，请先刷新核对，勿重复提交。",
    insufficient_credits: "模型额度不足，请升级或购买加量包。",
    unsupported_plan_change: "当前不支持此方案变更，请选择同周期的更高等级。",
    refund_requires_review: "该退款需要人工核实，请联系账户管理员。",
    unauthorized: "登录已过期，请重新登录。",
    forbidden: "你没有当前账户的操作权限。",
    conflict: "状态已变化或请求冲突，请刷新后核对。",
    invalid_request: "输入无效，请检查后重试。",
    payment_not_configured: "真实支付尚未开通。",
    not_found: "记录不存在或不属于当前账户。",
    service_unavailable: "服务暂时不可用，请稍后重试。",
    manual_review_required: "该退款需要人工核实，请联系账户管理员。",
    insufficient_funds: "钱包余额不足。",
    unsupported: "当前不支持该方案变更。",
  };
  return (
    texts[code] ||
    (status === 401
      ? texts.unauthorized
      : status === 403
        ? texts.forbidden
        : `请求未完成${status ? `（${status}）` : "，请检查网络连接。"}`)
  );
}
export async function request<T>(
  path: string,
  options: {
    method?: string;
    body?: unknown;
    csrf?: string;
    key?: string;
    signal?: AbortSignal;
  } = {},
): Promise<T> {
  const method = options.method || "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") {
    if (!options.csrf) throw new Error("安全凭证已失效，请刷新页面。");
    headers["X-CSRF-Token"] = options.csrf;
  }
  if (options.key) headers["Idempotency-Key"] = options.key;
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
  });
  const data =
    response.status === 204
      ? undefined
      : await response.json().catch(() => undefined);
  if (!response.ok) throw new ApiError(response.status, data?.error || "");
  return data as T;
}
export const accountPath = (id: string, resource: string) =>
  `/v1/accounts/${encodeURIComponent(id)}/${resource}`;
export const idempotencyKey = () => crypto.randomUUID();
