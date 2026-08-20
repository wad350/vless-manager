import SpeedIcon from "@mui/icons-material/Speed";
import { useMemo } from "react";
import {
  CrudPage,
  renderOptionChain,
  renderOptionLabel,
  type CrudConfig,
} from "../components/CrudPage";
import { useApi } from "../auth/AuthContext";
import type {
  BandwidthLimiter,
  BandwidthLimiterCreate,
  BandwidthLimiterUpdate,
  BandwidthMode,
  BandwidthStrategy,
  ConnectionType,
} from "../api/types";
import {
  parseSquadIds,
  pickSquadIds,
  renderSquadIds,
  squadIdsField,
  useSquadCatalog,
} from "./squadField";

// Display labels mirror service/admin_panel/tables/bandwidth_limiter.go so
// the table shows "Global" instead of "global", "Download" instead of
// "download", etc.
const STRATEGIES: { value: BandwidthStrategy; label: string }[] = [
  { value: "global", label: "Global" },
  { value: "connection", label: "Connection" },
  { value: "bypass", label: "Bypass" },
];
const MODES: { value: BandwidthMode; label: string }[] = [
  { value: "download", label: "Download" },
  { value: "upload", label: "Upload" },
  { value: "bidirectional", label: "Bidirectional" },
];
const CONN_TYPES: { value: ConnectionType; label: string }[] = [
  { value: "hwid", label: "HWID" },
  { value: "mux", label: "Mux" },
  { value: "source_ip", label: "Source IP" },
  { value: "default", label: "Default" },
];
const FLOW_KEYS: { value: string; label: string }[] = [
  { value: "user", label: "User" },
  { value: "source_ip", label: "Source IP" },
  { value: "hwid", label: "HWID" },
  { value: "mux", label: "Mux" },
  { value: "protocol", label: "Protocol" },
  { value: "destination", label: "Destination" },
];

export function BandwidthLimitersPage() {
  const api = useApi();
  const squads = useSquadCatalog(api);
  // Memoise the CRUD config so CrudPage's `reload` callback stays stable
  // across re-renders. Recomputed when the API client flips or a new
  // squad name is merged into the catalog through observeRows.
  const config = useMemo<
    CrudConfig<BandwidthLimiter, BandwidthLimiterCreate, BandwidthLimiterUpdate>
  >(() => ({
    title: "Bandwidth limiters",
    icon: <SpeedIcon />,
    idKey: "id",
    onRowsChange: (rows) => squads.observeRows(rows, pickSquadIds),
    columns: [
      { key: "id", label: "ID" },
      // squad_ids is an array column — `sortable: false` matches the
      // legacy admin where these columns lacked FieldSortable(). Render
      // squad names instead of raw IDs.
      {
        key: "squad_ids",
        label: "Squads",
        sortable: false,
        render: renderSquadIds<BandwidthLimiter>(squads.names),
      },
      { key: "username", label: "Username" },
      { key: "outbound", label: "Outbound" },
      { key: "strategy", label: "Strategy", render: renderOptionLabel<BandwidthLimiter>("strategy", STRATEGIES) },
      {
        key: "connection_type",
        label: "Connection type",
        render: renderOptionLabel<BandwidthLimiter>("connection_type", CONN_TYPES),
      },
      { key: "mode", label: "Mode", render: renderOptionLabel<BandwidthLimiter>("mode", MODES) },
      {
        key: "flow_keys",
        label: "Flow keys",
        sortable: false,
        render: renderOptionChain<BandwidthLimiter>("flow_keys", FLOW_KEYS),
      },
      {
        key: "speed",
        label: "Speed",
        // bypass disables the limiter's speed cap server-side
        // (excluded_if=Strategy bypass on the DTO), so the row's `speed`
        // arrives as an empty string. Render an unambiguous ∞ instead so
        // the column reads as "no cap" at a glance instead of looking
        // like missing data.
        render: (row) => (row.strategy === "bypass" ? "∞" : row.speed),
      },
      { key: "created_at", label: "Created at" },
      { key: "updated_at", label: "Updated at" },
    ],
    filters: [
      { name: "username", label: "Username", type: "text" },
      { name: "outbound", label: "Outbound", type: "text" },
      { name: "strategy", label: "Strategy", type: "select", options: STRATEGIES },
      { name: "connection_type", label: "Connection type", type: "select", options: CONN_TYPES },
      { name: "mode", label: "Mode", type: "select", options: MODES },
      { name: "created_at", label: "Created at", type: "datetime-range" },
      { name: "updated_at", label: "Updated at", type: "datetime-range" },
    ],
    fields: [
      // Mirror service/admin_panel/tables/bandwidth_limiter.go: squads,
      // username and outbound are locked once the limiter exists.
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
        // (excluded_if=Strategy bypass on the DTO). connection_type is
        // additionally only meaningful for the "connection" strategy. Wipe
        // every dependent field on change so a stale value can't be
        // smuggled out of a hidden field.
        clears: ["connection_type", "mode", "flow_keys", "speed"],
      },
      {
        name: "connection_type",
        label: "Connection type",
        type: "select",
        options: CONN_TYPES,
        visibleWhen: (form) => form.strategy === "connection",
      },
      {
        name: "mode",
        label: "Mode",
        type: "select",
        required: true,
        options: MODES,
        visibleWhen: (form) => form.strategy !== "bypass",
      },
      {
        name: "flow_keys",
        label: "Flow keys",
        type: "multiselect",
        options: FLOW_KEYS,
        // Render the picked values as an ordered pipeline
        // ("User → Destination → IP") so the form's preview matches the
        // table column and reads as a queue rather than a flat set.
        displayAsChain: true,
        visibleWhen: (form) => form.strategy !== "bypass",
      },
      {
        name: "speed",
        label: "Speed",
        type: "text",
        required: true,
        helperText: "e.g. 2MB, 100KB, 1GB or 10Mbps",
        visibleWhen: (form) => form.strategy !== "bypass",
      },
    ],
    list: (q) => api.bandwidthLimiters.list(q),
    count: (q) => api.bandwidthLimiters.count(q),
    create: (b) => api.bandwidthLimiters.create(b),
    update: (id, b) => api.bandwidthLimiters.update(Number(id), b),
    remove: (id) => api.bandwidthLimiters.remove(Number(id)),
    fromEntity: (e) => ({
      squad_ids: e.squad_ids,
      username: e.username ?? "",
      outbound: e.outbound,
      strategy: e.strategy,
      connection_type: e.connection_type ?? "",
      mode: e.mode,
      flow_keys: e.flow_keys ?? [],
      speed: e.speed,
    }),
    toCreate: (f) => bodyFor(f) as unknown as BandwidthLimiterCreate,
    toUpdate: (f, original) => updateBodyFor(f, original) as unknown as BandwidthLimiterUpdate,
  }), [api, squads]);
  return (
    <CrudPage<BandwidthLimiter, BandwidthLimiterCreate, BandwidthLimiterUpdate> config={config} />
  );
}

// strategyDependentFields returns connection_type / mode / flow_keys /
// speed only when the picked strategy is *not* "bypass". The manager-api
// DTO carries `excluded_if=Strategy bypass` on each of these, and the
// SQL repository unconditionally parses `Speed` via
// byteformats.NetworkBytesCompat (see service/manager/repository/sqlite/
// repository.go) — so sending `mode: ""` / `speed: ""` with a bypass
// payload makes the server reject the request with 400 "invalid
// format". Dropping the keys entirely (returned `undefined` is omitted
// by `JSON.stringify`) keeps the body shape exactly what the DTO
// expects for each strategy branch.
function strategyDependentFields(
  f: Record<string, unknown>,
  strategy: BandwidthStrategy,
) {
  if (strategy === "bypass") {
    return {
      connection_type: undefined,
      mode: undefined,
      flow_keys: undefined,
      speed: undefined,
    };
  }
  return {
    connection_type: f.connection_type
      ? (String(f.connection_type) as ConnectionType)
      : undefined,
    mode: String(f.mode ?? "") as BandwidthMode,
    flow_keys:
      Array.isArray(f.flow_keys) && (f.flow_keys as string[]).length > 0
        ? (f.flow_keys as string[])
        : undefined,
    speed: String(f.speed ?? "").trim(),
  };
}

function bodyFor(f: Record<string, unknown>) {
  const strategy = String(f.strategy ?? "") as BandwidthStrategy;
  return {
    squad_ids: parseSquadIds(f.squad_ids),
    username: f.username ? String(f.username) : undefined,
    outbound: String(f.outbound ?? "").trim(),
    strategy,
    ...strategyDependentFields(f, strategy),
  };
}

// updateBodyFor reuses the original entity for the create-only fields
// (username, outbound) that the API still requires on update.
function updateBodyFor(f: Record<string, unknown>, original: BandwidthLimiter) {
  const strategy = String(f.strategy ?? "") as BandwidthStrategy;
  return {
    username: original.username || undefined,
    outbound: original.outbound,
    strategy,
    ...strategyDependentFields(f, strategy),
  };
}
