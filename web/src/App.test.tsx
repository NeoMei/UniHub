// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import App from "./App";
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null,"","/");
  window.sessionStorage.clear();
});
it("keeps a product-linked team selected across login and never falls back to personal for an unavailable account", async () => {
  window.history.replaceState(null,"","/?account=team&product=superwagie");
  let authenticated=false;
  vi.stubGlobal("fetch",vi.fn(async(path:string)=>new Response(JSON.stringify(path==="/auth/session"?{authenticated,user:{id:"alice",name:"Alice"},sandbox:true}:path==="/v1/accounts"?[{id:"personal",name:"Personal",kind:"personal",role:"owner"},{id:"team",name:"Team",kind:"team",role:"owner"}]:[]),{status:200})));
  const first=render(<App/>);
  await screen.findByRole("link",{name:/登录归一/});
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
  const hero = await screen.findByRole("heading", { name: /一个身份/ });
  expect(hero.textContent).toContain("连接你的产品");
  expect(screen.getAllByRole("link", { name: "进入控制台" }).length).toBeGreaterThan(0);
  expect(screen.getByRole("link", { name: "产品能力" }).getAttribute("href")).toBe("#product");
  expect(screen.getByText("统一账户")).toBeDefined();
  expect(screen.getByText("跨产品协作")).toBeDefined();
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
      { name: "钱包与充值" },
    ),
  );
  await waitFor(() =>
    expect(
      screen.getByText("只有所有者和财务管理员可查看钱包与充值。"),
    ).toBeDefined(),
  );
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
  expect(guidance.textContent).toContain("当前付费账户的 llmrouter 产品钱包扣款");
  expect(guidance.textContent).toContain("额度不足不会自动转为现金扣款");
  expect(guidance.textContent).not.toContain("功能产品");
  expect(await screen.findByText("年付 · 纯功能订阅，不含模型额度")).toBeDefined();
  fireEvent.click(within(screen.getByRole("navigation", { name: "主导航" })).getByRole("button", { name: "产品会员" }));
  expect(screen.getByText("管理产品会员，查看已付期限与方案权益。")).toBeDefined();
  expect(screen.queryByText(/每月包含额度/)).toBeNull();
  expect(calls.some((path) => /billing-profile|\/(wallets|orders|settings|operations)$/.test(path))).toBe(false);
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
it("shows product-owned settings with an explicit UniHub ownership boundary", async () => {
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
  fireEvent.click(screen.getByRole("button", { name: "产品设置" }));
  expect(await screen.findByRole("heading", { name: "产品设置" })).toBeDefined();
  expect(screen.getAllByText("UniHub 管理")).toHaveLength(2);
  expect(screen.getAllByText("会员、模型额度、钱包与账单")).toHaveLength(2);
  expect(screen.getAllByText("产品内设置")).toHaveLength(2);
  expect(screen.getByText("模型提供商与 API Key")).toBeDefined();
  expect(screen.getByRole("link", { name: /打开 SuperWagie 设置/ }).getAttribute("href")).toBe("https://superwagie.quukk.com/settings");
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
});
