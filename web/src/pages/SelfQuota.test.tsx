import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as quotaAPI from "../api/quota";
import { _resetLocale, translate } from "../i18n";
import type { SelfQuotaResponse } from "../types";
import SelfQuota from "./SelfQuota";

(
  globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const payload: SelfQuotaResponse = {
  reset_allowed: true,
  quota: {
    plan_type: "team",
    allowed: true,
    windows: [
      {
        kind: "five_hour",
        used_percent: -5,
        remaining_percent: 130,
        limit_window_seconds: 18_000,
        reset_at: "2030-01-01T05:00:00Z",
      },
      {
        kind: "weekly",
        used_percent: 75,
        remaining_percent: 25,
        limit_window_seconds: 604_800,
        reset_at: "2030-01-07T00:00:00Z",
      },
    ],
    reset_bank: {
      status: "available",
      available_count: 2,
      total_earned_count: 5,
      next_expiry: "2030-01-10T00:00:00Z",
    },
    fetched_at: "2030-01-01T00:00:00Z",
  },
};

let container: HTMLDivElement;
let root: Root;

function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

async function submitKey(key: string): Promise<void> {
  const input = container.querySelector<HTMLInputElement>("#quota-key");
  const form = container.querySelector<HTMLFormElement>("form");
  expect(input).not.toBeNull();
  expect(form).not.toBeNull();
  await act(async () => {
    setInputValue(input!, key);
    form!.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    );
    await Promise.resolve();
  });
}

beforeEach(() => {
  _resetLocale("en");
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root.render(<SelfQuota />));
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

describe("SelfQuota", () => {
  it("loads the bound quota, clamps percentages, and clears the key on logout", async () => {
    const fetchQuota = vi
      .spyOn(quotaAPI, "fetchSelfQuota")
      .mockResolvedValue(payload);

    await submitKey("cpa_user_secret");

    expect(fetchQuota).toHaveBeenCalledWith("cpa_user_secret");
    expect(container.textContent).toContain("team");
    expect(container.textContent).toContain("2");
    expect(container.textContent).not.toContain("cpa_user_secret");

    const progress = Array.from(
      container.querySelectorAll<HTMLElement>("[role=progressbar]"),
    );
    expect(progress).toHaveLength(2);
    expect(progress[0].getAttribute("aria-valuenow")).toBe("100");
    expect(progress[1].getAttribute("aria-valuenow")).toBe("25");

    const logout = container.querySelector<HTMLButtonElement>(
      `button[aria-label="${translate("selfQuota.logout")}"]`,
    );
    expect(logout).not.toBeNull();
    act(() => logout!.click());

    const input = container.querySelector<HTMLInputElement>("#quota-key");
    expect(input).not.toBeNull();
    expect(input?.value).toBe("");
    expect(container.textContent).not.toContain("team");
  });

  it("hides reset controls when the administrator has not authorized the key", async () => {
    vi.spyOn(quotaAPI, "fetchSelfQuota").mockResolvedValue({
      ...payload,
      reset_allowed: false,
    });
    const resetQuota = vi.spyOn(quotaAPI, "resetSelfQuota");

    await submitKey("cpa_read_only_secret");

    expect(
      container.querySelector<HTMLButtonElement>(
        `button[aria-label="${translate("selfQuota.resetNow")}"]`,
      ),
    ).toBeNull();
    expect(container.textContent).toContain(
      translate("selfQuota.resetNotAuthorized"),
    );
    expect(resetQuota).not.toHaveBeenCalled();
  });

  it("confirms and submits one idempotent manual reset", async () => {
    const fetchQuota = vi
      .spyOn(quotaAPI, "fetchSelfQuota")
      .mockResolvedValue(payload);
    const resetQuota = vi.spyOn(quotaAPI, "resetSelfQuota").mockResolvedValue({
      reset: { code: "reset", windows_reset: 1 },
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);

    await submitKey("cpa_user_secret");
    const resetButton = container.querySelector<HTMLButtonElement>(
      `button[aria-label="${translate("selfQuota.resetNow")}"]`,
    );
    expect(resetButton).not.toBeNull();
    await act(async () => {
      resetButton!.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(confirm).toHaveBeenCalledWith(translate("selfQuota.resetConfirm"));
    expect(resetQuota).toHaveBeenCalledWith(
      "cpa_user_secret",
      expect.stringMatching(/^[a-f0-9]{32}$/),
    );
    expect(fetchQuota).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain(
      translate("selfQuota.resetSuccess"),
    );
  });

  it("does not reset when confirmation is cancelled", async () => {
    vi.spyOn(quotaAPI, "fetchSelfQuota").mockResolvedValue(payload);
    const resetQuota = vi.spyOn(quotaAPI, "resetSelfQuota");
    vi.spyOn(window, "confirm").mockReturnValue(false);

    await submitKey("cpa_user_secret");
    const resetButton = container.querySelector<HTMLButtonElement>(
      `button[aria-label="${translate("selfQuota.resetNow")}"]`,
    );
    act(() => resetButton!.click());
    expect(resetQuota).not.toHaveBeenCalled();
  });

  it("shows a scoped reset error without leaving the dashboard", async () => {
    vi.spyOn(quotaAPI, "fetchSelfQuota").mockResolvedValue(payload);
    vi.spyOn(quotaAPI, "resetSelfQuota").mockRejectedValue(
      new quotaAPI.QuotaAPIError(409, "quota_reset_no_credit", "no credit"),
    );
    vi.spyOn(window, "confirm").mockReturnValue(true);

    await submitKey("cpa_user_secret");
    const resetButton = container.querySelector<HTMLButtonElement>(
      `button[aria-label="${translate("selfQuota.resetNow")}"]`,
    );
    await act(async () => {
      resetButton!.click();
      await Promise.resolve();
    });

    expect(container.querySelector("[role=alert]")?.textContent).toBe(
      translate("selfQuota.resetErrorNoCredit"),
    );
    expect(container.textContent).toContain("team");
    expect(container.textContent).not.toContain("cpa_user_secret");
  });

  it("shows the downstream-key error without exposing the key", async () => {
    vi.spyOn(quotaAPI, "fetchSelfQuota").mockRejectedValue(
      new quotaAPI.QuotaAPIError(401, "unauthorized", "invalid key"),
    );

    await submitKey("cpa_invalid_secret");

    const alert = container.querySelector<HTMLElement>("[role=alert]");
    expect(alert?.textContent).toBe(translate("selfQuota.errorUnauthorized"));
    expect(container.textContent).not.toContain("cpa_invalid_secret");
  });

  it("requires a non-empty key before making a request", async () => {
    const fetchQuota = vi.spyOn(quotaAPI, "fetchSelfQuota");

    await submitKey("   ");

    expect(fetchQuota).not.toHaveBeenCalled();
    expect(
      container.querySelector<HTMLElement>("[role=alert]")?.textContent,
    ).toBe(translate("selfQuota.keyRequired"));
  });
});
