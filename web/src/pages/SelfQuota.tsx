import { useRef, useState, type FormEvent } from "react";
import {
  Activity,
  Eye,
  EyeOff,
  KeyRound,
  LogOut,
  RefreshCw,
  RotateCcw,
} from "lucide-react";
import { fetchSelfQuota, QuotaAPIError } from "../api/quota";
import { getLocale, useT } from "../i18n";
import type { QuotaWindow, SelfQuotaResponse } from "../types";

function clampPercent(value: number): number {
  return Math.min(100, Math.max(0, value));
}

function percent(value: number): string {
  return `${Math.round(clampPercent(value))}%`;
}

function formatTime(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat(getLocale(), {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function errorText(error: unknown, t: ReturnType<typeof useT>): string {
  if (!(error instanceof QuotaAPIError)) return t("selfQuota.errorUnavailable");
  switch (error.code) {
    case "unauthorized":
      return t("selfQuota.errorUnauthorized");
    case "quota_binding_invalid":
      return t("selfQuota.errorBinding");
    case "quota_credential_not_unique":
      return t("selfQuota.errorNotUnique");
    case "quota_credential_unavailable":
    case "quota_credential_invalid":
    case "quota_credential_incomplete":
      return t("selfQuota.errorCredential");
    default:
      return t("selfQuota.errorUnavailable");
  }
}

function WindowPanel({ window }: { window: QuotaWindow }) {
  const t = useT();
  const label = window.kind === "five_hour"
    ? t("selfQuota.fiveHour")
    : t("selfQuota.weekly");
  return (
    <section className={`quota-window quota-window-${window.kind}`}>
      <div className="quota-window-head">
        <div>
          <div className="quota-eyebrow">{label}</div>
          <div className="quota-percent">{percent(window.remaining_percent)}</div>
        </div>
        <Activity size={22} aria-hidden="true" />
      </div>
      <div
        className="quota-progress"
        role="progressbar"
        aria-label={`${label}: ${t("selfQuota.remaining")}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(clampPercent(window.remaining_percent))}
      >
        <span style={{ width: percent(window.remaining_percent) }} />
      </div>
      <div className="quota-window-meta">
        <span>{t("selfQuota.used")}: {percent(window.used_percent)}</span>
        <span>{t("selfQuota.resetAt")}: {formatTime(window.reset_at)}</span>
      </div>
    </section>
  );
}

function QuotaDashboard({
  data,
  busy,
  onRefresh,
  onLogout,
}: {
  data: SelfQuotaResponse;
  busy: boolean;
  onRefresh: () => void;
  onLogout: () => void;
}) {
  const t = useT();
  const fiveHour = data.quota.windows.find((window) => window.kind === "five_hour");
  const weekly = data.quota.windows.find((window) => window.kind === "weekly");
  const reset = data.quota.reset_bank;
  const state = data.quota.allowed === false
    ? { className: "blocked", label: t("selfQuota.blocked") }
    : data.quota.allowed === true
      ? { className: "ready", label: t("selfQuota.available") }
      : { className: "unknown", label: t("selfQuota.unknown") };
  return (
    <div className="quota-page">
      <header className="quota-topbar">
        <div className="quota-brand">
          <span className="quota-brand-mark"><Activity size={20} aria-hidden="true" /></span>
          <div>
            <div className="quota-brand-title">{t("selfQuota.title")}</div>
          </div>
        </div>
        <div className="quota-actions">
          <button
            type="button"
            className="quota-icon-btn"
            onClick={onRefresh}
            disabled={busy}
            title={t("selfQuota.refresh")}
            aria-label={t("selfQuota.refresh")}
          >
            <RefreshCw size={18} className={busy ? "quota-spin" : ""} aria-hidden="true" />
          </button>
          <button
            type="button"
            className="quota-icon-btn"
            onClick={onLogout}
            title={t("selfQuota.logout")}
            aria-label={t("selfQuota.logout")}
          >
            <LogOut size={18} aria-hidden="true" />
          </button>
        </div>
      </header>

      <main className="quota-main">
        <div className="quota-account-line">
          <span>{t("selfQuota.plan")}</span>
          <strong>{data.quota.plan_type || t("selfQuota.unknown")}</strong>
          <span className={`quota-state ${state.className}`}>
            {state.label}
          </span>
        </div>

        <div className="quota-window-grid">
          {fiveHour ? <WindowPanel window={fiveHour} /> : <div className="quota-empty-window">{t("selfQuota.fiveHourUnavailable")}</div>}
          {weekly ? <WindowPanel window={weekly} /> : <div className="quota-empty-window">{t("selfQuota.weeklyUnavailable")}</div>}
        </div>

        <section className="quota-reset">
          <div className="quota-reset-head">
            <div>
              <div className="quota-eyebrow">{t("selfQuota.resetBank")}</div>
              <h2>{t("selfQuota.resetCredits")}</h2>
            </div>
            <RotateCcw size={22} aria-hidden="true" />
          </div>
          {reset.status === "unavailable" ? (
            <div className="quota-reset-note">{t("selfQuota.resetUnavailable")}</div>
          ) : (
            <>
              {reset.status === "summary" ? (
                <div className="quota-reset-note">{t("selfQuota.resetSummary")}</div>
              ) : null}
              <div className="quota-reset-stats">
                <div>
                  <span>{t("selfQuota.resetAvailable")}</span>
                  <strong>{reset.available_count ?? "-"}</strong>
                </div>
                <div>
                  <span>{t("selfQuota.resetEarned")}</span>
                  <strong>{reset.total_earned_count ?? "-"}</strong>
                </div>
                <div>
                  <span>{t("selfQuota.nextExpiry")}</span>
                  <strong className="quota-reset-time">{formatTime(reset.next_expiry)}</strong>
                </div>
              </div>
            </>
          )}
        </section>

        <div className="quota-fetched">
          {t("selfQuota.updatedAt")}: {formatTime(data.quota.fetched_at)}
        </div>
      </main>
    </div>
  );
}

export default function SelfQuota() {
  const t = useT();
  const [key, setKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [data, setData] = useState<SelfQuotaResponse | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const requestGeneration = useRef(0);

  const load = async (plainKey: string) => {
    const generation = ++requestGeneration.current;
    setBusy(true);
    setError("");
    try {
      const nextData = await fetchSelfQuota(plainKey);
      if (generation === requestGeneration.current) {
        setData(nextData);
        setShowKey(false);
      }
    } catch (loadError) {
      if (generation === requestGeneration.current) {
        setData(null);
        setError(errorText(loadError, t));
      }
    } finally {
      if (generation === requestGeneration.current) {
        setBusy(false);
      }
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!key.trim()) {
      setError(t("selfQuota.keyRequired"));
      return;
    }
    void load(key);
  };

  if (data) {
    return (
      <QuotaDashboard
        data={data}
        busy={busy}
        onRefresh={() => void load(key)}
        onLogout={() => {
          requestGeneration.current++;
          setKey("");
          setShowKey(false);
          setData(null);
          setError("");
          setBusy(false);
        }}
      />
    );
  }

  return (
    <div className="quota-login-page">
      <div className="quota-login-brand">
        <span className="quota-brand-mark"><Activity size={22} aria-hidden="true" /></span>
        <div>
          <h1>{t("selfQuota.title")}</h1>
          <p>{t("selfQuota.signIn")}</p>
        </div>
      </div>
      <form className="quota-login-card" onSubmit={submit}>
        <label htmlFor="quota-key">{t("selfQuota.keyLabel")}</label>
        <div className="quota-key-field">
          <KeyRound size={18} aria-hidden="true" />
          <input
            id="quota-key"
            type={showKey ? "text" : "password"}
            value={key}
            onChange={(event) => setKey(event.target.value)}
            placeholder={t("selfQuota.keyPlaceholder")}
            autoComplete="off"
            spellCheck={false}
            autoFocus
          />
          <button
            type="button"
            className="quota-field-icon"
            onClick={() => setShowKey((visible) => !visible)}
            title={showKey ? t("selfQuota.hideKey") : t("selfQuota.showKey")}
            aria-label={showKey ? t("selfQuota.hideKey") : t("selfQuota.showKey")}
          >
            {showKey ? <EyeOff size={18} aria-hidden="true" /> : <Eye size={18} aria-hidden="true" />}
          </button>
        </div>
        {error && <div className="quota-error" role="alert">{error}</div>}
        <button className="quota-submit" type="submit" disabled={busy}>
          {busy ? <RefreshCw size={18} className="quota-spin" aria-hidden="true" /> : <Activity size={18} aria-hidden="true" />}
          {busy ? t("selfQuota.loading") : t("selfQuota.open")}
        </button>
        <p className="quota-memory-note">{t("selfQuota.memoryOnly")}</p>
      </form>
    </div>
  );
}
