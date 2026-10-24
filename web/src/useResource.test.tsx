// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useResource } from "./useResource";
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
describe("account request isolation", () => {
  it("clears data immediately and ignores late responses from the previous account even if transport ignores abort", async () => {
    const pending = new Map<string, (value: Response) => void>();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (path: string) =>
          new Promise<Response>((resolve) => pending.set(path, resolve)),
      ),
    );
    const hook = renderHook(
      ({ path }) => useResource<{ account_id: string }>(path),
      { initialProps: { path: "/v1/accounts/A/credits" } },
    );
    hook.rerender({ path: "/v1/accounts/B/credits" });
    await act(async () => {
      pending.get("/v1/accounts/B/credits")!(
        new Response(JSON.stringify({ account_id: "B" }), { status: 200 }),
      );
    });
    await waitFor(() => expect(hook.result.current.data?.account_id).toBe("B"));
    await act(async () => {
      pending.get("/v1/accounts/A/credits")!(
        new Response(JSON.stringify({ account_id: "A" }), { status: 200 }),
      );
    });
    expect(hook.result.current.data?.account_id).toBe("B");
    hook.rerender({ path: "/v1/accounts/C/credits" });
    expect(hook.result.current.data).toBeUndefined();
  });
});
