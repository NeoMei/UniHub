export type ApiErrorBody = {
  error: string;
  password_issues?: string[];
  retry_after?: number;
  registration_not_started?: boolean;
};
const passwordIssues: Record<string, string> = {
  too_short: "新密码至少 8 个字符。",
  too_many_bytes: "新密码的 UTF-8 编码不能超过 72 字节。",
  control_character: "新密码不能含控制字符。",
  letter_case_and_digit_required: "新密码须包含大小写字母和数字。",
  special_character_required: "新密码须包含特殊字符。",
  repeated_character: "组织策略不允许重复字符。",
  unsupported_policy: "请联系管理员核对组织密码策略。",
};
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    public body?: ApiErrorBody,
    public retryAfter?: number,
  ) {
    const guidance = body?.password_issues?.map(issue => passwordIssues[issue]).filter(Boolean).join(" ");
    super([errorText(code, status), guidance, code === "rate_limited" && retryAfter !== undefined ? `请在 ${retryAfter} 秒后重试。` : ""].filter(Boolean).join(" "));
  }
}
export function errorText(code: string, status = 0): string {
  const texts: Record<string, string> = {
    operation_pending: "当前账户有待核实操作，请先刷新核对，勿重复提交。",
    credit_rates_unavailable: "暂无可用的 CREDIT 模型费率，暂不能购买；请联系管理员配置费率。",
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
    invalid_password: "密码未通过验证，请核对当前密码及组织的新密码规则。",
    rate_limited: "请求过于频繁，请稍后重试。",
    identity_unavailable: "身份服务暂时不可用；若刚提交密码变更，请重新登录核对，勿连续重试。",
    credentials_managed_by_product: "登录凭据由原产品管理，请前往原产品修改。",
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
  if (!response.ok) {
    const code = typeof data?.error === "string" ? data.error : "";
    // Keep only public contract fields. Never retain arbitrary provider errors,
    // request contents or credentials in a browser error object.
    const body: ApiErrorBody = { error: code };
    if (Array.isArray(data?.password_issues)) body.password_issues = data.password_issues.filter((issue: unknown) => typeof issue === "string" && Object.hasOwn(passwordIssues, issue));
    if (Number.isSafeInteger(data?.retry_after) && data.retry_after >= 0) body.retry_after = data.retry_after;
    if (typeof data?.registration_not_started === "boolean") body.registration_not_started = data.registration_not_started;
    const header = response.headers.get("Retry-After");
    const headerSeconds = header && /^\d+$/.test(header) ? Number(header) : header ? Math.max(0, Math.ceil((Date.parse(header) - Date.now()) / 1000)) : undefined;
    const wait = Number.isSafeInteger(headerSeconds) ? headerSeconds : body.retry_after;
    throw new ApiError(response.status, code, body, wait);
  }
  return data as T;
}
export const accountPath = (id: string, resource: string) =>
  `/v1/accounts/${encodeURIComponent(id)}/${resource}`;
export const idempotencyKey = () => crypto.randomUUID();
