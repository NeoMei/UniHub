// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterAll, afterEach, beforeAll, expect, it, vi } from "vitest";
import App from "./App";
beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  HTMLDialogElement.prototype.close = function () { this.open = false; };
});
afterAll(() => {
  Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
  Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null,"","/");
  window.sessionStorage.clear();
});
it("explains missing central administration authority on a product user-management link", async () => {
  window.history.replaceState(null, "", "/?view=user-management");
  vi.stubGlobal("fetch", vi.fn(async (path: string) => new Response(JSON.stringify(
    path === "/auth/session" ? { authenticated: true, user: { id: "member", name: "Member" }, sandbox: true, csrf_token: "csrf" }
    : path === "/v1/user-management" ? { enabled: true, can_manage: false }
    : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }] : []
  ), { status: 200 })));
  render(<App />);
  expect(await screen.findByText("当前账户没有 UniHub 用户管理权限。")).toBeDefined();
  expect(screen.getByText(/个人或团队账户的所有者身份不等于平台管理员/)).toBeDefined();
  expect(screen.queryByText("搜索用户")).toBeNull();
});
it("hides system maintenance without confirmed platform permission", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => new Response(JSON.stringify(
    path === "/auth/session" ? { authenticated: true, user: { id: "member", name: "Member" }, sandbox: true, csrf_token: "csrf" }
    : path === "/admin/v1/me" ? { enabled: false, user_id: "member", roles: [], permissions: [], version: 0 }
    : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }] : []
  ), { status: 200 })));
  render(<App />);
  await screen.findByRole("heading", { name: "账户概览" });
  expect(screen.queryByRole("button", { name: "系统维护" })).toBeNull();
});
it("retains the allowlisted user-management destination through a signed-out console login", async () => {
  window.history.replaceState(null, "", "/?view=user-management");
  let authenticated = false;
  vi.stubGlobal("fetch", vi.fn(async (path: string) => new Response(JSON.stringify(
    path === "/auth/session" ? { authenticated, user: { id: "member", name: "Member" }, sandbox: true, csrf_token: "csrf" }
    : path === "/v1/user-management" ? { enabled: true, can_manage: false }
    : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }] : []
  ), { status: 200 })));
  const beforeLogin = render(<App />);
  await screen.findAllByRole("link", { name: /登录归一/ });
  beforeLogin.unmount();
  window.history.replaceState(null, "", "/");
  authenticated = true;
  const afterLogin = render(<App />);
  expect(await screen.findByText("当前账户没有 UniHub 用户管理权限。")).toBeDefined();
  afterLogin.unmount();
  // The destination hint is consumed once authentication is established.
  render(<App />);
  expect(await screen.findByRole("heading", { name: "账户概览" })).toBeDefined();
});
it("keeps a product-linked team selected across login and never falls back to personal for an unavailable account", async () => {
  window.history.replaceState(null,"","/?account=team&product=superwagie");
  let authenticated=false;
  vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path==="/auth/session"?{authenticated,user:{id:"alice",name:"Alice"},sandbox:true}:path==="/v1/accounts"?[{id:"personal",name:"Personal",kind:"personal",role:"owner"},{id:"team",name:"Team",kind:"team",role:"owner"}]:[]),{status:200})));
  const first=render(<App/>);
  await screen.findAllByRole("link",{name:/登录归一/});
  first.unmount();
  // OIDC returns to the root; the public account hint must survive in this tab.
  window.history.replaceState(null,"","/"); authenticated=true;
  const second=render(<App/>);
  await screen.findByRole("option",{name:"Personal · 个人"});
  await waitFor(()=>expect((screen.getByLabelText("当前付费账户") as HTMLSelectElement).value).toBe("team"));
  fireEvent.change(screen.getByLabelText("当前付费账户"),{target:{value:"personal"}});
  expect(new URLSearchParams(window.location.search).get("account")).toBe("personal");
  second.unmount();
  window.history.replaceState(null,"","/?account=revoked");
  render(<App/>);
  await screen.findByText("所选账户不可访问，请明确选择一个账户。");
  expect((screen.getByLabelText("当前付费账户") as HTMLSelectElement).value).toBe("revoked");
});
it("shows the UniHub product portal before login", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const data = path === "/auth/session"
      ? { authenticated: false, sandbox: true }
      : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  const hero = await screen.findByRole("heading", { name: /一个身份，/ });
  expect(hero.textContent).toContain("连接你的产品");
  expect(screen.getAllByRole("link", { name: "登录归一（UniHub）" }).length).toBeGreaterThan(0);
  expect(screen.getByRole("link", { name: "使用指南" }).getAttribute("href")).toBe("/guide");
  expect(screen.queryByRole("link", { name: "产品能力" })).toBeNull();
  expect(screen.queryByRole("link", { name: "使用方式" })).toBeNull();
  expect(screen.queryByRole("link", { name: "设置边界" })).toBeNull();
  expect(screen.getByText("一个身份")).toBeDefined();
  expect(screen.getByText("一套关系")).toBeDefined();
});
it("provides a real usage guide route from the public portal", async () => {
  window.history.replaceState(null, "", "/guide");
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const data = path === "/auth/session" ? { authenticated: false, sandbox: true } : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  expect(await screen.findByRole("heading", { name: "从账户开始，把每一步走清楚" })).toBeDefined();
  expect(screen.getByText("选择账户")).toBeDefined();
  expect(screen.getByText("在 UniHub 完成")).toBeDefined();
  expect(screen.getByText("SuperWagie")).toBeDefined();
  expect(screen.getAllByText(/模型路由、供应商凭证/).length).toBeGreaterThan(0);
  expect(screen.getByRole("heading", { name: "产品功能与使用方法" })).toBeDefined();
  expect(screen.getAllByText("核心功能").length).toBe(3);
  expect(screen.getAllByText("开始使用").length).toBe(3);
  fireEvent.click(screen.getByRole("link", { name: "首页" }));
  expect(await screen.findByRole("heading", { name: /一个身份，/ })).toBeDefined();
});
it("lets an authenticated user open the product portal and return to the console", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: true, user: { id: "u1", name: "Alice" }, csrf_token: "csrf" }
      : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }]
      : path === "/v1/me/profile" ? { id: "u1", username: "alice", display_name: "Alice", email: "alice@example.com", version: "1" }
      : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  await screen.findByRole("heading", { name: "账户概览" });
  const portalNav = within(screen.getByRole("navigation", { name: "产品站导航" }));
  expect(portalNav.getByRole("button", { name: "使用指南" })).toBeDefined();
  fireEvent.click(portalNav.getByRole("button", { name: "使用指南" }));
  expect(await screen.findByRole("heading", { name: "从账户开始，把每一步走清楚" })).toBeDefined();
  fireEvent.click(screen.getByRole("link", { name: "控制台" }));
  expect(await screen.findByRole("heading", { name: "账户概览" })).toBeDefined();
  fireEvent.click(screen.getByRole("link", { name: /归一 UniHub/ }));
  expect(await screen.findByRole("heading", { name: /一个身份，/ })).toBeDefined();
  expect(screen.getByRole("link", { name: /Alice/ })).toBeDefined();
  fireEvent.click(screen.getAllByRole("link", { name: "进入控制台" })[0]);
  expect(await screen.findByRole("heading", { name: "账户概览" })).toBeDefined();
});
it("keeps team member cash and finance APIs out of the browser while allowing credits and memberships", async () => {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      calls.push(path);
      const data =
        path === "/auth/session"
          ? {
              authenticated: true,
              sandbox: true,
              user: { id: "member", name: "成员", email: "" },
              csrf_token: "csrf",
            }
          : path === "/v1/accounts"
            ? [{ id: "team", name: "协作团队", kind: "team", role: "member" }]
            : [];
      return new Response(JSON.stringify(data), { status: 200 });
    }),
  );
  render(<App />);
  await screen.findByText("仅财务角色可查看");
  await waitFor(() => expect(calls).toContain("/v1/accounts/team/credits"));
  expect(calls).toContain("/v1/accounts/team/subscriptions");
  expect(
    calls.some((p) => /\/(wallets|orders|settings|operations)$/.test(p)),
  ).toBe(false);
  fireEvent.click(
    within(screen.getByRole("navigation", { name: "主导航" })).getByRole(
      "button",
      { name: "模型余额与充值" },
    ),
  );
  await waitFor(() =>
    expect(
      screen.getByText("只有所有者和财务管理员可查看钱包与充值。"),
    ).toBeDefined(),
  );
});
it("opens actor-scoped model consumption from a member's bills without requesting team financial resources", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    calls.push(path);
    const data = path === "/auth/session" ? { authenticated: true, sandbox: true, user: { id: "member", name: "Member" }, csrf_token: "csrf" }
      : path === "/v1/accounts" ? [{ id: "team", name: "Team", kind: "team", role: "member" }]
      : path.includes("/calls?") ? { scope: { account_id: "team", visibility: "actor", actor_id: "member" }, items: [], financial_details: false, filter: { from: "2026-10-01T00:00:00Z", to: "2026-10-31T00:00:00Z" }, total: "0", unknown_time: "0", has_more: false }
      : path.includes("usage/summary") ? { financial_details: false, calls_total: "0", settled_calls: "0", pending_calls: "0", unknown_calls: "0", usage_complete: true }
      : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  await screen.findByRole("heading", { name: "账户概览" });
  fireEvent.click(within(screen.getByRole("navigation", { name: "主导航" })).getByRole("button", { name: "账单" }));
  fireEvent.click(screen.getByRole("button", { name: "查看模型消费" }));
  expect(await screen.findByText("当前条件下暂无模型消费")).toBeDefined();
  expect(screen.getByRole("heading", { name: "模型消费" })).toBeDefined();
  expect(calls.some(path => path.startsWith("/v1/accounts/team/calls?"))).toBe(true);
  expect(calls.some(path => /\/(wallets|orders|settings|operations)$/.test(path))).toBe(false);
});
it("shows product-owned prepaid guidance and zero-credit memberships from existing reads", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    calls.push(path);
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: false, user: { id: "member", name: "成员" } }
      : path === "/v1/accounts"
        ? [{ id: "team", name: "协作团队", kind: "team", role: "member" }]
        : path === "/v1/products"
          ? [
              { id: "cash", name: "现金按量产品", billing_profile: {
                product_id: "cash", version: 1,
                subscription: { enabled: false }, included_credits: { enabled: false }, addons: { enabled: false },
                usage: { enabled: true, require_subscription: false, funding_modes: ["prepaid_cash"], default_funding_mode: "prepaid_cash" },
              } },
              { id: "features", name: "功能产品" },
            ]
          : path === "/v1/accounts/team/subscriptions"
            ? [{ id: "subscription", product_id: "features", status: "active", paid_through: "2099-01-01T00:00:00Z", offer: { name: "功能年费", period: "year", monthly_credit: "0" } }]
            : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  const guidance = await screen.findByText(/现金按量产品 已启用现金按量服务/);
  expect(guidance.textContent).toContain("使用入口由各产品自己的界面提供");
  expect(guidance.textContent).toContain("实际选择的计费模式和调用记录");
  expect(guidance.textContent).not.toContain("llmrouter 产品钱包扣款");
  expect(guidance.textContent).toContain("额度不足不会自动转为现金扣款");
  expect(guidance.textContent).not.toContain("功能产品");
  expect(await screen.findByText("年付 · 纯功能订阅，不含模型额度")).toBeDefined();
  fireEvent.click(within(screen.getByRole("navigation", { name: "主导航" })).getByRole("button", { name: "产品会员" }));
  expect(screen.getByText("管理产品会员，查看已付期限与方案权益。")).toBeDefined();
  expect(screen.queryByText(/每月包含额度/)).toBeNull();
  expect(calls.some((path) => /billing-profile|\/(wallets|orders|settings|operations)$/.test(path))).toBe(false);
});
it("blocks formal purchase actions when real payments are unavailable", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: false, user: { id: "owner", name: "Owner" }, csrf_token: "csrf" }
      : path === "/v1/accounts"
        ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }]
        : path === "/admin/v1/me"
          ? { enabled: false, user_id: "owner", roles: [], permissions: [], version: 0 }
          : path === "/v1/capabilities"
            ? { real_payments_configured: false, product_wallet_recharge: false, unified_wallet_recharge: false }
            : path === "/v1/catalog"
              ? [{ id: "plan-basic", product_id: "notes", name: "基础方案", account_kind: "both", kind: "membership", period: "month", price: "1000000", monthly_credit: "1000000", entitlements: {}, rank: 1, validity_months: 1, refund_rule: "unused_only_v1", status: "published", sandbox: false }]
              : path === "/v1/products"
                ? [{ id: "notes", name: "Notes" }]
                : path === "/v1/accounts/personal/subscriptions"
                  ? []
                  : path === "/v1/accounts/personal/credits"
                    ? []
                    : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  fireEvent.click(await screen.findByRole("button", { name: "产品会员" }));
  const purchase = await screen.findByRole("button", { name: "购买" });
  expect(
    (await screen.findAllByRole("status")).some((node) =>
      node.textContent?.includes("真实支付尚未开通"),
    ),
  ).toBe(true);
  expect((purchase as HTMLButtonElement).disabled).toBe(true);
});
it("loads the user profile and shows avatar plus account settings menu", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: false, user: { id: "u1", name: "Alice" }, csrf_token: "csrf" }
      : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }]
      : path === "/v1/me/profile" ? { id: "u1", username: "alice", display_name: "Alice Zhang", avatar_url: "/v1/me/avatar", email: "alice@example.com", version: "1" }
      : [];
    return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  render(<App />);
  const avatar = await screen.findByRole("img", { name: "Alice Zhang" });
  expect(avatar.getAttribute("src")).toBe("/v1/me/avatar?version=1");
  fireEvent.click(screen.getByRole("button", { name: /账户设置/ }));
  expect(await screen.findByRole("heading", { name: "账户设置" })).toBeDefined();
  expect(screen.getByText("alice@example.com")).toBeDefined();
});
it("does not expose a product settings page in the account console", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: false, user: { id: "u1", name: "Alice" }, csrf_token: "csrf" }
      : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }]
      : path === "/v1/me/profile" ? { id: "u1", username: "alice", display_name: "Alice Zhang", email: "alice@example.com", version: "1" }
      : path === "/v1/products" ? [{ id: "superwagie", name: "SuperWagie" }, { id: "agentwiki", name: "AgentWiki" }]
      : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  await screen.findByRole("heading", { name: "账户概览" });
  expect(screen.queryByRole("button", { name: "产品设置" })).toBeNull();
  expect(screen.queryByRole("button", { name: "已接入产品" })).toBeNull();
  expect(screen.queryByText("产品设置在哪里完成")).toBeNull();
});
it("saves the display name and submits password changes from account settings", async () => {
  const calls: Array<{ path: string; init?: RequestInit }> = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string, init?: RequestInit) => {
    calls.push({ path, init });
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: false, user: { id: "u1", name: "Alice" }, csrf_token: "csrf" }
      : path === "/v1/accounts" ? [{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }]
      : path === "/v1/me/profile" && init?.method === "PATCH" ? { id: "u1", username: "alice", display_name: "New Alice", email: "alice@example.com", version: "2" }
      : path === "/v1/me/profile" ? { id: "u1", username: "alice", display_name: "Alice", email: "alice@example.com", version: "1" }
      : path === "/v1/me/password" ? undefined
      : [];
    return new Response(data === undefined ? null : JSON.stringify(data), { status: path === "/v1/me/password" ? 204 : 200 });
  }));
  render(<App />);
  await screen.findByRole("heading", { name: "账户概览" });
  fireEvent.click(screen.getByRole("button", { name: /账户设置/ }));
  fireEvent.change(screen.getByLabelText("显示名称"), { target: { value: "New Alice" } });
  fireEvent.click(screen.getByRole("button", { name: "保存个人资料" }));
  await waitFor(() => expect(calls.some((call) => call.path === "/v1/me/profile" && call.init?.method === "PATCH")).toBe(true));
  fireEvent.change(screen.getByLabelText("当前密码"), { target: { value: "old-password" } });
  fireEvent.change(screen.getByLabelText("新密码"), { target: { value: "new-password" } });
  fireEvent.change(screen.getByLabelText("确认新密码"), { target: { value: "new-password" } });
  fireEvent.click(screen.getByRole("button", { name: "更新密码" }));
  await waitFor(() => expect(calls.some((call) => call.path === "/v1/me/password" && call.init?.method === "POST")).toBe(true));
  expect(await screen.findByRole("link", { name: "重新登录" })).toBeDefined();
  expect(screen.queryByRole("navigation", { name: "主导航" })).toBeNull();
  expect(screen.queryByLabelText("当前密码")).toBeNull();
});

it("selects a newly created team after the create confirmation completes", async () => {
  let created = false;
  vi.stubGlobal("fetch", vi.fn(async (path: string, init?: RequestInit) => {
    if (path === "/v1/accounts" && init?.method === "POST") {
      created = true;
      return new Response(JSON.stringify({ id: "team-new", name: "新团队", kind: "team", role: "owner" }), { status: 201 });
    }
    const data = path === "/auth/session"
      ? { authenticated: true, sandbox: true, user: { id: "u1", name: "Alice" }, csrf_token: "csrf" }
      : path === "/v1/accounts"
        ? [
            { id: "personal", name: "个人账户", kind: "personal", role: "owner" },
            ...(created ? [{ id: "team-new", name: "新团队", kind: "team", role: "owner" }] : []),
          ]
        : path === "/v1/me/profile"
          ? { id: "u1", username: "alice", display_name: "Alice", email: "alice@example.com", version: "1" }
          : [];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<App />);
  await screen.findByRole("heading", { name: "账户概览" });
  fireEvent.click(screen.getByText("更多账户功能"));
  fireEvent.click(within(screen.getByRole("navigation", { name: "主导航" })).getByRole("button", { name: "团队与邀请" }));
  fireEvent.change(screen.getByLabelText("团队名称"), { target: { value: "新团队" } });
  fireEvent.click(screen.getByRole("button", { name: "创建团队" }));
  fireEvent.click(screen.getByRole("button", { name: "确认提交" }));
  await waitFor(() => expect((screen.getByLabelText("当前付费账户") as HTMLSelectElement).value).toBe("team-new"));
});

it("shows trusted product credential management for a native-only central account", async () => {
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>{
  const data=path==="/auth/session"?{authenticated:true,sandbox:false,user:{id:"native",name:"Native"},csrf_token:"csrf"}:path==="/v1/accounts"?[{id:"personal",name:"个人账户",kind:"personal",role:"owner"}]:path==="/v1/me/profile"?{id:"native",username:"native",display_name:"Native",email:"native@example.test",version:"1",credential_source:"product",credential_management:[{product_id:"llmrouter",instance_id:"default",url:"https://router.example.test/profile"}]}:[];
  return new Response(JSON.stringify(data),{status:200});
 }));
 render(<App/>);await screen.findByRole("heading",{name:"账户概览"});fireEvent.click(screen.getByRole("button",{name:/账户设置/}));
 expect(await screen.findByText("native@example.test")).toBeDefined();
 expect(screen.queryByLabelText("当前密码")).toBeNull();
 expect(screen.queryByRole("button",{name:"保存个人资料"})).toBeNull();
 expect(screen.getByRole("link",{name:"前往 llmrouter 管理密码与二次验证"}).getAttribute("href")).toBe("https://router.example.test/profile");
});

function settingsFixture(overrides: Record<string, unknown> = {}) {
  const calls: Array<{path: string; method: string}> = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string, init?: RequestInit) => {
    calls.push({path, method: init?.method || "GET"});
    const data = path in overrides ? overrides[path]
      : path === "/auth/session" ? {authenticated: true, sandbox: true, csrf_token: "csrf", user: {id: "u", name: "Test"}}
      : path === "/v1/accounts" ? [{id: "A", name: "账户 A", kind: "personal", role: "owner"}, {id: "B", name: "账户 B", kind: "team", role: "owner"}]
      : path === "/v1/products" ? [{id: "llmrouter", name: "LLMRouter"}, {id: "superwagie", name: "SuperWagie"}]
      : path === "/v1/capabilities" ? {product_wallet_recharge: true, unified_wallet_recharge: true}
      : path === "/admin/v1/me" ? {enabled: false, roles: [], permissions: []}
      : [];
    return new Response(JSON.stringify(data), {status: 200});
  }));
  return calls;
}
it.each([
  ["wallets", "llmrouter", "模型余额与充值"], ["wallets", "superwagie", "模型余额与充值"],
  ["memberships", "superwagie", "产品会员"], ["bills", "", "账单"], ["consumption", "", "模型消费"],
])("opens the allowlisted %s task without writing finances", async (view, product, title) => {
  window.history.replaceState(null, "", `/?view=${view}&product=${product}`);
  const calls = settingsFixture();
  render(<App />);
  await screen.findByRole("heading", {name: title, level: 1});
  if (view === "wallets") await waitFor(() => expect((screen.getByLabelText("充值目标钱包") as HTMLSelectElement).value).toBe(product));
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("discards unsupported view hints rather than granting a destination", async () => {
  window.history.replaceState(null, "", "/?view=admin&product=intruder");
  const calls = settingsFixture();
  render(<App />);
  await screen.findByRole("heading", {name: "账户概览", level: 1});
  expect(screen.queryByRole("button", {name: "系统维护"})).toBeNull();
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("discards an account-bound confirmation when the payer changes", async () => {
  window.history.replaceState(null, "", "/?view=wallets&product=llmrouter");
  const calls = settingsFixture();
  render(<App />);
  await screen.findByRole("heading", {name: "模型余额与充值", level: 1});
  await waitFor(() => expect((screen.getByLabelText("充值目标钱包") as HTMLSelectElement).value).toBe("llmrouter"));
  fireEvent.change(screen.getByLabelText("充值金额（元）"), {target: {value: "10"}});
  fireEvent.click(screen.getByRole("button", {name: "创建测试充值单"}));
  expect(screen.getByRole("dialog")).toBeDefined();
  fireEvent.change(screen.getByLabelText("当前付费账户"), {target: {value: "B"}});
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
it("shows administration only after verified capability and keeps direct-route authorization", async () => {
  settingsFixture({"/admin/v1/me": {enabled: true, roles: ["admin"], permissions: ["*"]}});
  const view = render(<App />);
  expect(await screen.findByRole("button", {name: "系统维护"})).toBeDefined();
  view.unmount();
  window.history.replaceState(null, "", "/admin/overview");
  settingsFixture();
  render(<App />);
  expect(await screen.findByText("当前用户没有平台管理权限。")).toBeDefined();
});
it("keeps a wallet product hint scoped to that task when opening consumption", async () => {
  window.history.replaceState(null, "", "/?view=wallets&product=llmrouter");
  const calls = settingsFixture();
  render(<App />);
  await screen.findByRole("heading", {name: "模型余额与充值", level: 1});
  await waitFor(() => expect((screen.getByLabelText("充值目标钱包") as HTMLSelectElement).value).toBe("llmrouter"));
  fireEvent.click(within(screen.getByRole("navigation", {name: "主导航"})).getByRole("button", {name: "模型消费"}));
  await waitFor(() => expect(calls.some(call => call.path.includes("/calls?"))).toBe(true));
  await waitFor(() => expect((screen.getByLabelText("产品") as HTMLSelectElement).value).toBe(""));
  expect(calls.filter(call => call.path.includes("/calls?")).every(call => !new URL(call.path, "http://local").searchParams.has("origin_product_id"))).toBe(true);
});
it("selects the catalog-confirmed model wallet for the normal model-balance navigation", async () => {
  const calls = settingsFixture();
  render(<App />);
  await screen.findByRole("heading", {name: "账户概览", level: 1});
  fireEvent.click(within(screen.getByRole("navigation", {name: "主导航"})).getByRole("button", {name: "模型余额与充值"}));
  await waitFor(() => expect((screen.getByLabelText("充值目标钱包") as HTMLSelectElement).value).toBe("llmrouter"));
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
