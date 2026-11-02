import { useMemo, useState } from "react";
import { CheckCircle2, Copy, ShieldCheck } from "lucide-react";
import { Notice, Panel } from "../ui";

const scopeOptions = [
  ["openid", "识别统一用户"],
  ["profile", "读取展示名和头像"],
  ["email", "读取邮箱（按需）"],
  ["billing:read", "读取会员、额度和账单（按需）"],
] as const;

export function Onboarding({ sandbox = false }: { sandbox?: boolean }) {
  const [product, setProduct] = useState("");
  const [callback, setCallback] = useState("");
  const [origin, setOrigin] = useState("");
  const [profile, setProfile] = useState("hybrid");
  const [scopes, setScopes] = useState<string[]>(["openid", "profile"]);
  const [copied, setCopied] = useState(false);
  const valid = Boolean(product.trim() && callback.trim());
  const command = useMemo(() => {
    const args = ["unihubctl integration prepare", `--product ${product.trim() || "<product-id>"}`, `--callback ${callback.trim() || "<callback-url>"}`, `--scopes ${scopes.join(",")}`, `--billing-profile ${profile}`];
    if (origin.trim()) args.push(`--origin ${origin.trim()}`);
    if (sandbox) args.push("--sandbox");
    return args.join(" ");
  }, [callback, origin, product, profile, sandbox, scopes]);
  async function copyCommand() {
    try {
      await navigator.clipboard?.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      setCopied(false);
    }
  }
  function toggleScope(scope: string) {
    if (scope === "openid") return;
    setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);
  }
  return (
    <div className="admin-onboarding">
      <div className="admin-page-heading">
        <div><h1>三方接入工作台</h1><p>把产品、回调、最小权限和计费组合整理成一份可执行的接入计划。</p></div>
        <span className="admin-draft-badge">计划模式</span>
      </div>
      <Notice><ShieldCheck size={17} />不会在浏览器保存客户端密钥。应用需要在服务端完成凭证配置、回调校验和最终读回。</Notice>
      {sandbox && <Notice>测试环境 · 生成的命令带有 sandbox 标记，不代表正式支付已接入。</Notice>}
      <div className="admin-onboarding-grid">
        <Panel title="1. 产品信息">
          <div className="admin-form">
            <label>产品标识<input value={product} onChange={(event) => setProduct(event.target.value)} placeholder="例如 notes" /></label>
            <label>回调地址<input value={callback} onChange={(event) => setCallback(event.target.value)} placeholder="https://product.example.com/auth/callback" /></label>
            <label>允许来源（可选）<input value={origin} onChange={(event) => setOrigin(event.target.value)} placeholder="https://product.example.com" /></label>
          </div>
        </Panel>
        <Panel title="2. 最小权限">
          <fieldset className="admin-scope-list"><legend>接入范围</legend>{scopeOptions.map(([scope, copy]) => <label key={scope}><input type="checkbox" aria-label={scope} checked={scopes.includes(scope)} disabled={scope === "openid"} onChange={() => toggleScope(scope)} /><span><strong>{scope}</strong><small>{copy}</small></span></label>)}</fieldset>
          <p className="admin-form-hint">先使用身份范围；只有确实要在产品内展示账单时才申请 billing:read。</p>
        </Panel>
        <Panel title="3. 计费组合">
          <label>计费模式<select value={profile} onChange={(event) => setProfile(event.target.value)}><option value="hybrid">会员 + 模型额度</option><option value="prepaid">预付钱包按量</option><option value="subscription">纯功能订阅</option></select></label>
          <p className="admin-form-hint">这是计划初值，最终配置仍由平台管理员在计费配置中校验并发布。</p>
        </Panel>
        <Panel title="4. 服务端执行与读回">
          <ol className="admin-onboarding-checklist"><li><CheckCircle2 size={16} />登记产品 ID、回调地址和允许来源</li><li><CheckCircle2 size={16} />在服务端保存客户端密钥并完成 OIDC 授权码 + PKCE</li><li><CheckCircle2 size={16} />发布 BillingProfile 和商品草稿</li><li><CheckCircle2 size={16} />读回 capability、产品、方案和用户映射结果</li></ol>
          <Notice kind={valid ? "info" : "warning"}>{valid ? "字段已足够生成接入计划。" : "填写产品标识和回调地址后才能执行接入准备。"}</Notice>
        </Panel>
      </div>
      <Panel title="接入计划命令">
        <div className="admin-command"><code>{command}</code><button className="secondary" onClick={copyCommand}><Copy size={15} />{copied ? "已复制" : "复制命令"}</button></div>
        <div className="admin-onboarding-actions"><button disabled={!valid}>生成接入计划</button><span>应用需要在服务端完成，浏览器只生成参数和检查清单。</span></div>
      </Panel>
    </div>
  );
}
