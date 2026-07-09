import { useEffect, useMemo, useState } from "react";
import { fetchCatalog, formatTierLabel, groupByCatalog } from "../api/models";
import type { CatalogGroup } from "../api/models";
import type { ModelRule } from "../types";
import { useT } from "../i18n";

interface Props {
  // currently bound rules (for edit mode preselection)
  initial?: ModelRule[];
  // called whenever the selection changes
  onChange: (rules: ModelRule[]) => void;
}

// Multi-select picker over CPA's available models, grouped by provider and —
// for providers whose auth files carry a tier/plan identity (codex, antigravity)
// or a custom classify group — further split into subgroups (codex · free /
// 自定义 · vip …). Selecting a model under a group pins that group into the
// ModelRule, which the plugin Scheduler honors at runtime.

export default function ModelPicker({ initial, onChange }: Props) {
  const t = useT();
  const [groups, setGroups] = useState<CatalogGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>("");
  const [query, setQuery] = useState("");

  // Selection key: "provider|group|model" (all lowercased for dedupe matching).
  // group is "" for non-tiered providers. A model selected under two tiers is
  // two independent keys — authorizing it under "team" does not also authorize
  // it under "free".
  const [selected, setSelected] = useState<Set<string>>(() => {
    const s = new Set<string>();
    for (const r of initial ?? []) {
      const g = (r.group ?? "").toLowerCase();
      s.add(r.provider.toLowerCase() + "|" + g + "|" + r.target_model.toLowerCase());
    }
    return s;
  });

  useEffect(() => {
    let alive = true;
    (async () => {
      setLoading(true);
      setError("");
      try {
        // Pass the providers already bound to this key (edit-mode prefill) so
        // fetchCatalog keeps those channels visible even when their credential
        // has since been removed — the user can still see and uncheck their
        // rows. New-key mode has no initial rules, so only configured channels
        // appear.
        const selectedProviders = new Set(
          (initial ?? []).map((r) => r.provider.toLowerCase()),
        );
        const cat = await fetchCatalog(selectedProviders);
        if (!alive) return;
        setGroups(groupByCatalog(cat));
      } catch (e) {
        if (!alive) return;
        setError((e as Error).message || t("picker.loadFailed"));
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  // emit ModelRule[] whenever selection (or available groups) change.
  // Guard: do NOT emit before the catalog has loaded (groups is empty). On
  // mount, groups=[] would otherwise emit rules=[] and wipe any pre-filled
  // state the parent derived from `initial` (e.g. per-alias pricing on the
  // edit page). Selection can only map to real rules once groups exist, and
  // the user can't toggle anything before the catalog renders, so skipping
  // the empty-groups emit is safe and preserves edit-mode prefill.
  useEffect(() => {
    if (groups.length === 0) return;
    // Map every catalog (provider, group, model) that is currently selected.
    const covered = new Set<string>();
    const rules: ModelRule[] = [];
    for (const g of groups) {
      const gkey = (g.group ?? "").toLowerCase();
      for (const m of g.models) {
        const key = g.provider + "|" + gkey + "|" + m.toLowerCase();
        covered.add(key);
        if (selected.has(key)) {
          const rule: ModelRule = { alias: m, provider: g.provider, target_model: m };
          if (g.group) rule.group = g.group;
          rules.push(rule);
        }
      }
    }
    // Preserve selected entries the catalog no longer covers. This matters for
    // editing a key created before tier grouping shipped: its codex rows have
    // group="" but the new catalog lists codex only under tier subgroups, so
    // the "codex||model" selection key wouldn't match any checkbox. Without
    // this pass the row would be silently dropped on save, losing the model
    // from the key. Re-emitting the stale entry keeps it (as legacy "any auth
    // for the provider" — the plugin Scheduler defers when group is empty), so
    // upgrading never degrades an existing key. The user can re-pick it under a
    // tier to opt into isolation.
    for (const key of selected) {
      if (covered.has(key)) continue;
      const [provider, group, ...rest] = key.split("|");
      const model = rest.join("|");
      if (!provider || !model) continue;
      const rule: ModelRule = { alias: model, provider, target_model: model };
      if (group) rule.group = group;
      rules.push(rule);
    }
    onChange(rules);
  }, [selected, groups, onChange]);

  const filtered = useMemo(() => {
    if (!query.trim()) return groups;
    const q = query.trim().toLowerCase();
    return groups
      .map((g) => ({
        provider: g.provider,
        group: g.group,
        models: g.models.filter(
          (m) => m.toLowerCase().includes(q) || g.provider.includes(q) || (g.group ?? "").includes(q),
        ),
      }))
      .filter((g) => g.models.length > 0);
  }, [groups, query]);

  const keyOf = (g: CatalogGroup, model: string) =>
    g.provider + "|" + (g.group ?? "").toLowerCase() + "|" + model.toLowerCase();

  const toggle = (g: CatalogGroup, model: string) => {
    const key = keyOf(g, model);
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const selectAll = (g: CatalogGroup) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const m of g.models) next.add(keyOf(g, m));
      return next;
    });
  };

  const clearAll = (g: CatalogGroup) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const m of g.models) next.delete(keyOf(g, m));
      return next;
    });
  };

  if (loading) return <div className="muted">{t("picker.loading")}</div>;
  if (error) return <div className="error">{error}</div>;
  if (groups.length === 0)
    return <div className="muted">{t("picker.empty")}</div>;

  return (
    <div>
      <input
        className="input"
        placeholder={t("picker.searchPlaceholder")}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        style={{ marginBottom: 12 }}
      />
      <div className="muted" style={{ marginBottom: 8 }}>
        {t("picker.selected", { count: selected.size })}
      </div>
      {filtered.map((g) => {
        const groupLabel = g.group ? formatTierLabel(t, g.group) : "";
        const head = g.provider + (groupLabel ? " · " + groupLabel : "");
        const allSelected = g.models.every((m) => selected.has(keyOf(g, m)));
        return (
          <div className="picker-group" key={head}>
            <div className="pg-head">
              <span>{head}</span>
              <span className="pg-actions">
                <button type="button" className="btn sm" onClick={() => (allSelected ? clearAll(g) : selectAll(g))}>
                  {allSelected ? t("picker.clearAll") : t("picker.selectAll")}
                </button>
              </span>
            </div>
            <div className="pg-models">
              {g.models.map((m) => {
                const key = keyOf(g, m);
                const active = selected.has(key);
                return (
                  <label key={key} className={active ? "active" : ""}>
                    <input
                      type="checkbox"
                      checked={active}
                      onChange={() => toggle(g, m)}
                    />
                    {m}
                  </label>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}
