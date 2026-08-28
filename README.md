# cpa-key-policy-quota

Downstream **API key policy** plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

In plain words: you issue your own `cpa_…` keys to clients. Each key only sees the models you allow, can be rate-limited and budget-limited, and is routed to real CPA upstream providers (Codex, Claude, OpenAI-compat channels, etc.). CPA’s own `api-keys` can still exist for admin use — **do not put plugin-issued keys into `api-keys`**, or you bypass this plugin.

This repository is an independently maintained fork of [`origin652/cpa-plugin-key-policy`](https://github.com/origin652/cpa-plugin-key-policy). It keeps the upstream plugin ID (`cpa-key-policy`) for configuration compatibility and adds a downstream-key-authenticated Codex quota page.

| | |
|---|---|
| **Repo** | [ErzerLP/cpa-key-policy-quota](https://github.com/ErzerLP/cpa-key-policy-quota) |
| **Upstream** | [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) |
| **License** | MIT |
| **Install** | Build from source; the fork is not yet listed in the CLIProxyAPI Plugins Store |
| **中文说明** | [README.zh-CN.md](./README.zh-CN.md) |

---

## What it does (human version)

1. **Issue keys** — create many downstream keys; each has an allow-list of models (or shared aliases).
2. **Route** — client calls with alias name `fast`; plugin rewrites to e.g. `codex` + `gpt-5.4-mini`.
3. **Limit** — per-key RPM, optional daily/weekly USD caps, token or per-call billing.
4. **Isolate credentials (tiers / groups)** — pin a request to Codex free/team/… or to a **custom classify group** so it never lands on the wrong auth file.
5. **Multi-target aliases** — one alias can point at several backends (priority or round-robin).
6. **Web management UI** — manage keys, global aliases, and credential classification inside CPA.
7. **Self-service Codex quota** — let a downstream key view only its uniquely bound credential's 5-hour/weekly windows and Reset Bank summary; administrators may separately authorize manual weekly resets per key.

---

## Concepts

### Downstream key

A plugin-owned secret (`cpa_…`). Authenticated only by this plugin. Holds:

- allowed **models** and/or **aliases**
- RPM
- optional daily / weekly dollar limits
- optional `allow_models_endpoint` (see below)
- optional administrator-only `allow_quota_reset` capability (default `false`)

### Alias (global mapping table)

A reusable name like `fast` that expands to one or more **targets**:

| Field | Meaning |
|--------|---------|
| `provider` | CPA provider id (`codex`, `claude`, or an openai-compatibility **name** such as `cerebras`) |
| `target_model` | Real upstream model id |
| `group` | Optional credential filter (see [Credential groups](#credential-groups-tiers--classify)) |
| `dispatch` | `priority` (always first usable target) or `round-robin` |
| billing | `tokens` (per-million prices) or `per_call` (fixed USD) |

Keys can **reference** aliases instead of duplicating targets. Multi-target aliases expand to several rules with the same alias name; auth and routing share one pick per request so the `group` filter matches the chosen target.

### Credential groups (tiers + classify)

Two sources of “which auth file may serve this request”:

| Kind | How it appears in the picker | Stored in mapping as |
|------|------------------------------|----------------------|
| **Built-in tier** (Codex `plan_type`, Antigravity `tier`) | e.g. Free tier / Team | bare name: `free`, `team`, `supported` |
| **Custom classify rule** | e.g. **Custom · vip** | prefixed: `classify:vip` |

**Runtime rule:** if a mapping sets a group, the plugin scheduler **only** picks auth files in that group. No match → hard failure (`auth_not_found`), never silently fall back to another tier.

**Custom classification** (Web UI → Mapping → Credential Classification):

- Match auth-file fields (`filename`, `provider`, `plan_type`, `tier`, …) with a regex.
- Assign a **group name** you choose (stored bare on the rule).
- Catalog and mappings use `classify:<name>` so it never collides with built-in `free` / `team`.
- One file can match **multiple** custom groups (shown under each).
- If no custom rule matches → built-in tier (for Codex/Antigravity) or flat (no group) for other auth-file providers.
- OpenAI-compat / API-key channels stay **flat** (no groups).

Configure classify rules in the UI, or via management API (`/classify-rules`, `/classify-preview`, `/catalog`). You do not need to hand-edit state JSON for normal use.

### OpenAI-compatibility providers

Channels under CPA `openai-compatibility` (e.g. a named proxy) use the **channel name** as `provider`. The plugin maps it to CPA’s internal key `openai-compatible-<name>` when routing. Models must be listed on that channel in CPA config, or the host reports no auth for that model.

---

## Capabilities (plugin hooks)

| Hook | Role |
|------|------|
| Frontend auth | Know plugin keys; enforce alias allow-list, RPM, budget; stamp route + group metadata |
| Model router | Alias → provider + target model |
| Scheduler | When `group` is set, filter auth candidates by tier / `classify:` group |
| Response interceptor | Non-stream JSON: rewrite top-level `model` back to the alias |
| Usage | Token / per-call billing into the state file |
| Self-service quota | Downstream-key auth, unique `classify:` credential binding, redacted Codex quota and Reset Bank summary, separately authorized weekly reset |
| Management API + embedded Web UI | Keys, aliases, classify rules, status |

---

## Build

Linux `.so` needs cgo and a matching toolchain:

```bash
make test
make build-linux          # builds web UI, then linux amd64/arm64 .so
# or
make web-build
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared \
  -buildmode=c-shared -o dist/cpa-key-policy_linux_amd64.so ./cmd/cpa-key-policy
```

On Windows, build the `.so` via WSL/Linux. `go test ./...` uses a non-cgo stub so unit tests run without a shared-library toolchain.

Copy the `.so` into CPA `plugins.dir` and enable the plugin in config.

---

## Config

Minimal shape (see also [`config.example.yaml`](./config.example.yaml)):

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-policy:
      enabled: true
      priority: 10
      state_file: "cpa-key-policy-state.json"
```

Notes:

- If `state_file` exists, it is the source of truth for keys / aliases / classify rules / usage.
- Prefer creating keys and aliases in the **Web UI** or Management API; seed YAML `keys` is mainly for first boot.
- Never commit real key hashes, management secrets, or live host URLs into public docs.

---

## Web Management UI

Embedded in the plugin. After load, open:

```text
http://<your-cpa-host>:<api-port>/v0/resource/plugins/cpa-key-policy/index.html
```

Login with CPA **management** secret (`remote-management.secret-key` / management password). The secret stays in memory only (not `localStorage`); refresh → re-login.

UI areas:

| Tab / page | Use for |
|------------|---------|
| Keys | Create / edit / rotate / delete keys; bind models or aliases; RPM & budgets |
| Mapping → Aliases | Global multi-target aliases, dispatch, pricing |
| Mapping → Classification | Custom credential groups + match preview |
| Model picker | Catalog of providers; tier / **Custom · …** subgroups |

## Self-service Codex quota

Open the user-facing page without a CPA management secret:

```text
http://<your-cpa-host>:<api-port>/v0/resource/plugins/cpa-key-policy/quota.html
```

The user enters a plugin-issued `cpa_…` key. The browser sends it only in the `Authorization: Bearer` header to the fixed same-origin endpoint below; it is kept in React memory and is not written to `localStorage` or `sessionStorage`.

```text
GET /v0/resource/plugins/cpa-key-policy/quota/api
Authorization: Bearer cpa_…
```

A key is eligible only when:

- it has at least one effective Codex target;
- every effective Codex target uses the same custom `classify:<group>` binding;
- that classify group matches exactly one enabled, available Codex auth-file credential.

For example:

```text
Classify rule: filename ^codex-user-a\.json$ -> group user-a
Key target:    provider codex, group classify:user-a
```

Zero matches, multiple matches, built-in tier groups such as `team`, or ungrouped Codex targets are rejected. The query never falls back to another credential.

The response is redacted and includes only the plan, availability state, recognized 5-hour/weekly windows, reset times, Reset Bank summary, and the boolean reset capability for that downstream key. It never returns raw auth JSON, the credential filename, email, `auth_index`, account ID, access token, refresh token, ID token, or management secret. `total_earned_count` means credits granted by Reset Bank; it is **not** a reliable count of successful resets already used.

Quota viewing does not grant quota-reset permission. An administrator must explicitly enable **Allow weekly quota reset** for each key in the key form, or set `"allow_quota_reset": true` through the management API. The field defaults to `false`, including for keys loaded from older state files. A reset-disabled key receives `403 quota_reset_forbidden` before the plugin lists or reads any bound credential.

When both the administrator permission and an available Reset Bank credit are present, the page can manually reset the weekly quota. Current CLIProxyAPI resource routes accept only `GET`, so the compatibility endpoint requires both an explicit confirmation header and an idempotency key; opening or prefetching the URL cannot trigger a reset.

```text
GET /v0/resource/plugins/cpa-key-policy/quota/api/reset
Authorization: Bearer cpa_…
X-CPA-Quota-Reset-Confirm: reset-weekly-quota
Idempotency-Key: <unique 16-128 character request id>
```

The backend revalidates the key-to-credential binding, selects the earliest-expiring usable credit, permits only one in-flight reset per bound account, and reuses a deterministic upstream `redeem_request_id` for duplicate requests. A successful reset clears the quota cache; Codex may still take about one minute to publish the updated weekly window.

The feature requires a CLIProxyAPI plugin host that exposes `host.auth.list`, `host.auth.get`, and `host.http.do` callbacks.

Dev UI without rebuilding the `.so`:

```bash
cd web
npm install
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

---

## Management API (summary)

Exact paths (no path templates). Auth: CPA management bearer token.

**Keys**

- `GET/POST/PATCH/DELETE …/keys` (`id` in query or body for mutate)
- `POST …/keys/rotate?id=…`
- `POST …/keys/reset-rpm?id=…`
- `GET …/keys/usage?id=…`
- `GET …/status`

**Aliases**

- `GET/POST/DELETE …/aliases`

**Classify**

- `GET/POST/DELETE …/classify-rules`
- `POST …/classify-rules/reorder`
- `POST …/classify-preview` — group → credential ids (UI preview; bare group names)
- `POST …/catalog` — body: auth-file credentials + models; response: picker `entries` with `classify:` groups

Create key (plain key returned **once**):

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/keys" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "team-a",
    "name": "Team A",
    "rpm": 60,
    "allow_quota_reset": false,
    "models": [
      {"alias":"fast","provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

Create a multi-target alias:

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/aliases" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "alias": "cheap-chat",
    "dispatch": "priority",
    "billing_mode": "tokens",
    "targets": [
      {"provider":"cerebras","target_model":"gpt-oss-120b"},
      {"provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

---

## Client request behavior

| Case | Result |
|------|--------|
| Known key + allowed alias | Auth OK → route → optional group filter → upstream |
| Known key + unknown model | Auth rejected |
| RPM / budget exceeded | Rejected |
| Group set, no matching auth file | `auth_not_found` / unavailable (no cross-tier leak) |
| Unknown key | Plugin declines; CPA may try native `api-keys` |
| Non-stream chat response | Top-level `model` rewritten to alias |
| Stream | Body not rewritten (v1) |

### `/v1/models` on CPA main port

Per-key `allow_models_endpoint`: **binary** — deny (401) or full global list. CPA cannot filter that list per plugin key on the main port.

---

## Setup checklist

1. Build / install the `.so` into CPA `plugins.dir`.
2. Enable `plugins` + `cpa-key-policy` in CPA config; set `state_file`.
3. Open the Web UI with the management secret.
4. (Optional) Define **classify rules** if you need custom credential buckets.
5. Create **aliases** (multi-target / pricing) and/or pick models per key (with tier or Custom group).
6. Create keys, save the one-time `plain_key`, and hand it to clients.
7. For self-service quota, bind all Codex targets on that key to one unique `classify:` group and share the `/quota.html` URL.
8. Client: OpenAI-compatible base URL = CPA; `Authorization: Bearer cpa_…`; `model` = alias name.
9. Ensure openai-compat channels list the models you map; empty model lists → host “no auth” errors.

---

## Tests

```bash
go test ./...
cd web && npm run typecheck && npm test && npm run build
```
