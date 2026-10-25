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
  await screen.findByRole("link",{name:/登录 UniHub/});
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
