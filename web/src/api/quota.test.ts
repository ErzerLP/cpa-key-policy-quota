import { describe, expect, it, vi } from "vitest";
import { fetchSelfQuota, QuotaAPIError, SELF_QUOTA_API } from "./quota";

const payload = {
  quota: {
    plan_type: "team",
    windows: [],
    reset_bank: { status: "available", available_count: 2 },
    fetched_at: "2030-01-01T00:00:00Z",
  },
};

describe("fetchSelfQuota", () => {
  it("uses only a bearer key and disables browser credential storage", async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify(payload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    await expect(fetchSelfQuota(" cpa_user ", fetcher as typeof fetch)).resolves.toEqual(payload);
    expect(fetcher).toHaveBeenCalledWith(SELF_QUOTA_API, {
      method: "GET",
      headers: { Authorization: "Bearer cpa_user", Accept: "application/json" },
      cache: "no-store",
      credentials: "omit",
    });
  });

  it("rejects malformed successful responses", async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ quota: null }), { status: 200 }));
    await expect(fetchSelfQuota("cpa_user", fetcher as typeof fetch)).rejects.toMatchObject({
      status: 200,
      code: "quota_response_invalid",
    });
  });

  it("returns a typed, redacted API error", async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({
      error: { code: "quota_binding_invalid", message: "binding failed" },
    }), { status: 409 }));
    const request = fetchSelfQuota("cpa_user", fetcher as typeof fetch);
    await expect(request).rejects.toBeInstanceOf(QuotaAPIError);
    await expect(request).rejects.toMatchObject({ status: 409, code: "quota_binding_invalid" });
  });
});
