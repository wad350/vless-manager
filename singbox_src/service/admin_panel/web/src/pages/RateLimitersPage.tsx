import FilterAltIcon from "@mui/icons-material/FilterAlt";
import { useMemo } from "react";
import {
  CrudPage,
  renderOptionLabel,
  type CrudConfig,
} from "../components/CrudPage";
import { useApi } from "../auth/AuthContext";
import type {
  RateConnectionType,
  RateLimiter,
  RateLimiterCreate,
  RateLimiterUpdate,
  RateStrategy,
} from "../api/types";
import {
  parseSquadIds,
  pickSquadIds,
  renderSquadIds,
  squadIdsField,
  useSquadCatalog,
} from "./squadField";

// Display labels mirror service/admin_panel/tables/rate_limiter.go.
const STRATEGIES: { value: RateStrategy; label: string }[] = [
  { value: "fixed_window", label: "Fixed window" },
  { value: "sliding_window", label: "Sliding window" },
  { value: "token_bucket", label: "Token bucket" },
  { value: "leaky_bucket", label: "Leaky bucket" },
  { value: "bypass", label: "Bypass" },
];
const CONN_TYPES: { value: RateConnectionType; label: string }[] = [
  { value: "hwid", label: "HWID" },
  { value: "mux", label: "Mux" },
  { value: "source_ip", label: "Source IP" },
  { value: "default", label: "Default" },
];

export function RateLimitersPage() {
  const api = useApi();
  const squads = useSquadCatalog(api);
  // Memoise the CRUD config so CrudPage's `reload` callback stays stable
  // across re-renders. Recomputed when the API client flips or a new
  // squad name is merged into the catalog through observeRows.
  const config = useMemo<
    CrudConfig<RateLimiter, RateLimiterCreate, RateLimiterUpdate>
  >(() => ({
    title: "Rate limiters",
    icon: <FilterAltIcon />,
    idKey: "id",
    onRowsChange: (rows) => squads.observeRows(rows, pickSquadIds),
    columns: [
      { key: "id", label: "ID" },
      {
        key: "squad_ids",
        label: "Squads",
        sortable: false,
        render: renderSquadIds<RateLimiter>(squads.names),
      },
      { key: "username", label: "Username" },
      { key: "outbound", label: "Outbound" },
      { key: "strategy", label: "Strategy", render: renderOptionLabel<RateLimiter>("strategy", STRATEGIES) },
      {
        key: "connection_type",
        label: "Connection type",
        render: renderOptionLabel<RateLimiter>("connection_type", CONN_TYPES),
      },
      {
        key: "count",
        label: "Count",
        // bypass disables the limiter server-side
        // (excluded_if=Strategy bypass on the DTO), so `count` arrives
        // as 0. Render an unambiguous ∞ instead so the column reads as
        // "no cap" at a glance instead of looking like a real zero.
        render: (row) => (row.strategy === "bypass" ? "∞" : row.count),
      },
      {
        key: "interval",
        label: "Interval",
        // Same as `count`: `interval` is excluded_if=Strategy bypass and
        // arrives empty for bypass rows.
        render: (row) => (row.strategy === "bypass" ? "∞" : row.interval),
      },
      { key: "created_at", label: "Created at" },
      { key: "updated_at", label: "Updated at" },
    ],
    filters: [
      { name: "username", label: "Username", type: "text" },
      { name: "outbound", label: "Outbound", type: "text" },
      { name: "strategy", label: "Strategy", type: "select", options: STRATEGIES },
      { name: "connection_type", label: "Connection type", type: "select", options: CONN_TYPES },
      { name: "interval", label: "Interval", type: "text", placeholder: "e.g. 1s, 10s, 1m" },
      { name: "created_at", label: "Created at", type: "datetime-range" },
      { name: "updated_at", label: "Updated at", type: "datetime-range" },
    ],
    fields: [
      // Mirror service/admin_panel/tables/rate_limiter.go: squads, username
      // and outbound are locked once the limiter exists.
      squadIdsField(squads.loadOptions),
      { name: "username", label: "Username", type: "text", only: "create" },
      { name: "outbound", label: "Outbound", type: "text", required: true, only: "create" },
      {
        name: "strategy",
        label: "Strategy",
        type: "select",
        required: true,
        options: STRATEGIES,
        // bypass disables every post-Strategy field server-side
        // (excluded_if=Strategy bypass on the DTO). Wipe their values when
        // switching so a stale entry can't be smuggled out of a hidden field.
        clears: ["connection_type", "count", "interval"],
      },
      {
        name: "connection_type",
        label: "Connection type",
        type: "select",
        required: true,
        options: CONN_TYPES,
        visibleWhen: (form) => form.strategy !== "bypass",
      },
      {
        name: "count",
        label: "Count",
        type: "number",
        required: true,
        visibleWhen: (form) => form.strategy !== "bypass",
      },
      {
        name: "interval",
        label: "Interval",
        type: "text",
        required: true,
        helperText: "e.g. 1s",
        visibleWhen: (form) => form.strategy !== "bypass",
      },
    ],
    list: (q) => api.rateLimiters.list(q),
    count: (q) => api.rateLimiters.count(q),
    create: (b) => api.rateLimiters.create(b),
    update: (id, b) => api.rateLimiters.update(Number(id), b),
    remove: (id) => api.rateLimiters.remove(Number(id)),
    fromEntity: (e) => ({
      username: e.username ?? "",
      outbound: e.outbound,
      strategy: e.strategy,
      connection_type: e.connection_type,
      count: e.count,
      interval: e.interval,
    }),
    // `connection_type` / `count` / `interval` carry
    // `excluded_if=Strategy bypass` on the manager-api DTO and the
    // SQL repository unconditionally parses `interval` via
    // time.ParseDuration — sending `interval: ""` with a bypass
    // payload would make the server reject the request with 400
    // "invalid format". Drop the keys entirely on bypass so
    // `JSON.stringify` skips them.
    toCreate: (f) => {
      const strategy = String(f.strategy ?? "") as RateStrategy;
      const bypass = strategy === "bypass";
      return {
        squad_ids: parseSquadIds(f.squad_ids),
        username: f.username ? String(f.username) : undefined,
        outbound: String(f.outbound ?? "").trim(),
        strategy,
        connection_type: bypass
          ? undefined
          : (String(f.connection_type ?? "") as RateConnectionType),
        count: bypass ? undefined : Number(f.count ?? 0),
        interval: bypass ? undefined : String(f.interval ?? "").trim(),
      };
    },
    toUpdate: (f, original) => {
      const strategy = String(f.strategy ?? "") as RateStrategy;
      const bypass = strategy === "bypass";
      return {
        username: original.username || undefined,
        outbound: original.outbound,
        strategy,
        connection_type: bypass
          ? undefined
          : (String(f.connection_type ?? "") as RateConnectionType),
        count: bypass ? undefined : Number(f.count ?? 0),
        interval: bypass ? undefined : String(f.interval ?? "").trim(),
      };
    },
  }), [api, squads]);
  return <CrudPage<RateLimiter, RateLimiterCreate, RateLimiterUpdate> config={config} />;
}
