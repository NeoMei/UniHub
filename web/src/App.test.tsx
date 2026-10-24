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
