import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, request } from "./api";
afterEach(() => vi.unstubAllGlobals());
describe("browser API boundary", () => {
  it("retains only safe password guidance and Retry-After without exposing provider data", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      error: "invalid_password", password_issues: ["too_short", "special_character_required"],
      provider_token: "private", password: "private",
    }), { status: 400, headers: { "Retry-After": "45" } })));
    const error = await request("/v1/me/password", { method: "POST", csrf: "csrf" }).catch(e => e);
    if (!(error instanceof ApiError)) throw new Error("Expected a structured API error");
    expect(error.body).toEqual({ error: "invalid_password", password_issues: ["too_short", "special_character_required"] });
    expect(error.retryAfter).toBe(45);
    expect(error.message).toContain("至少 8 个字符");
    expect(error.message).toContain("特殊字符");
    expect(error.message).not.toContain("private");
  });
  it("explains rate limits with the server wait instead of suggesting immediate resubmission", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response('{"error":"rate_limited","retry_after":90}', { status: 429 })));
    await expect(request("/v1/me/password")).rejects.toThrow("90 秒");
  });
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
