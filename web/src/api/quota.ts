import type {
  QuotaResetBank,
  QuotaWindow,
  SelfQuotaResetResponse,
  SelfQuotaResponse,
} from "../types";

export const SELF_QUOTA_API = "/v0/resource/plugins/cpa-key-policy/quota/api";
export const SELF_QUOTA_RESET_API = `${SELF_QUOTA_API}/reset`;
export const QUOTA_RESET_CONFIRM_HEADER = "X-CPA-Quota-Reset-Confirm";
export const QUOTA_RESET_CONFIRM_VALUE = "reset-weekly-quota";

export class QuotaAPIError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "QuotaAPIError";
    this.status = status;
    this.code = code;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isOptionalFiniteNumber(value: unknown): value is number | undefined {
  return (
    value === undefined || (typeof value === "number" && Number.isFinite(value))
  );
}

function isQuotaWindow(value: unknown): value is QuotaWindow {
  if (!isRecord(value)) return false;
  return (
    (value.kind === "five_hour" || value.kind === "weekly") &&
    typeof value.used_percent === "number" &&
    Number.isFinite(value.used_percent) &&
    typeof value.remaining_percent === "number" &&
    Number.isFinite(value.remaining_percent) &&
    typeof value.limit_window_seconds === "number" &&
    Number.isFinite(value.limit_window_seconds) &&
    (value.reset_at === undefined || typeof value.reset_at === "string")
  );
}

function isResetBank(value: unknown): value is QuotaResetBank {
  if (!isRecord(value)) return false;
  return (
    (value.status === "available" ||
      value.status === "summary" ||
      value.status === "unavailable") &&
    isOptionalFiniteNumber(value.available_count) &&
    isOptionalFiniteNumber(value.total_earned_count) &&
    (value.next_expiry === undefined || typeof value.next_expiry === "string")
  );
}

function isSelfQuotaResponse(value: unknown): value is SelfQuotaResponse {
  if (!isRecord(value) || !isRecord(value.quota)) return false;
  const quota = value.quota;
  return (
    typeof value.reset_allowed === "boolean" &&
    (quota.plan_type === undefined || typeof quota.plan_type === "string") &&
    (quota.allowed === undefined || typeof quota.allowed === "boolean") &&
    Array.isArray(quota.windows) &&
    quota.windows.every(isQuotaWindow) &&
    isResetBank(quota.reset_bank) &&
    typeof quota.fetched_at === "string"
  );
}

function isSelfQuotaResetResponse(
  value: unknown,
): value is SelfQuotaResetResponse {
  if (!isRecord(value) || !isRecord(value.reset)) return false;
  return (
    (value.reset.code === "reset" || value.reset.code === "already_redeemed") &&
    typeof value.reset.windows_reset === "number" &&
    Number.isInteger(value.reset.windows_reset) &&
    value.reset.windows_reset >= 0
  );
}

function errorPayload(
  value: unknown,
): { code?: string; message?: string } | undefined {
  if (!isRecord(value) || !isRecord(value.error)) return undefined;
  return {
    code: typeof value.error.code === "string" ? value.error.code : undefined,
    message:
      typeof value.error.message === "string" ? value.error.message : undefined,
  };
}

export async function fetchSelfQuota(
  key: string,
  fetcher: typeof fetch = fetch,
): Promise<SelfQuotaResponse> {
  const response = await fetcher(SELF_QUOTA_API, {
    method: "GET",
    headers: {
      Authorization: `Bearer ${key.trim()}`,
      Accept: "application/json",
    },
    cache: "no-store",
    credentials: "omit",
  });

  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const error = errorPayload(payload);
    throw new QuotaAPIError(
      response.status,
      error?.code ?? "quota_request_failed",
      error?.message ?? `HTTP ${response.status}`,
    );
  }
  if (!isSelfQuotaResponse(payload)) {
    throw new QuotaAPIError(
      response.status,
      "quota_response_invalid",
      "invalid quota response",
    );
  }
  return payload;
}

export async function resetSelfQuota(
  key: string,
  idempotencyKey: string,
  fetcher: typeof fetch = fetch,
): Promise<SelfQuotaResetResponse> {
  const response = await fetcher(SELF_QUOTA_RESET_API, {
    method: "GET",
    headers: {
      Authorization: `Bearer ${key.trim()}`,
      Accept: "application/json",
      [QUOTA_RESET_CONFIRM_HEADER]: QUOTA_RESET_CONFIRM_VALUE,
      "Idempotency-Key": idempotencyKey,
    },
    cache: "no-store",
    credentials: "omit",
  });

  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const error = errorPayload(payload);
    throw new QuotaAPIError(
      response.status,
      error?.code ?? "quota_reset_failed",
      error?.message ?? `HTTP ${response.status}`,
    );
  }
  if (!isSelfQuotaResetResponse(payload)) {
    throw new QuotaAPIError(
      response.status,
      "quota_reset_response_invalid",
      "invalid quota reset response",
    );
  }
  return payload;
}
