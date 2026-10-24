import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, request } from "./api";
afterEach(() => vi.unstubAllGlobals());
describe("browser API boundary", () => {
  it("sends same-origin cookies, CSRF and stable idempotency key without bearer credentials", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 202 }));
    vi.stubGlobal("fetch", fetcher);
    await request("/v1/accounts/A/orders", {
      method: "POST",
      csrf: "csrf-safe",
      key: "same-attempt",
      body: { offer_id: "published" },
    });
    const [, options] = fetcher.mock.calls[0];
    expect(options.credentials).toBe("same-origin");
    expect(options.headers["X-CSRF-Token"]).toBe("csrf-safe");
    expect(options.headers["Idempotency-Key"]).toBe("same-attempt");
    expect(options.headers.Authorization).toBeUndefined();
  });
  it("refuses a mutation without CSRF before network activity", async () => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    await expect(
      request("/v1/accounts/A/orders", { method: "POST" }),
    ).rejects.toThrow("安全凭证");
    expect(fetcher).not.toHaveBeenCalled();
  });
  it("preserves ambiguous error status instead of treating it as success", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response('{"error":"service_unavailable"}', { status: 503 }),
        ),
    );
    await expect(request("/v1/accounts/A/orders")).rejects.toBeInstanceOf(
      ApiError,
    );
  });
});
