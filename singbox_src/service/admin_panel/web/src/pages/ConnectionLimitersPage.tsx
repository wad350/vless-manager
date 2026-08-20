import LinkIcon from "@mui/icons-material/Link";
import { useMemo } from "react";
import {
  CrudPage,
  renderOptionLabel,
  type CrudConfig,
} from "../components/CrudPage";
import { useApi } from "../auth/AuthContext";
import type {
  ConnectionLimiter,
  ConnectionLimiterCreate,
  ConnectionLimiterUpdate,
  ConnectionStrategy,
  ConnectionType,
  LockType,
} from "../api/types";
import {
  parseSquadIds,
  pickSquadIds,
  renderSquadIds,
  squadIdsField,
  useSquadCatalog,
} from "./squadField";

// Display labels mirror service/admin_panel/tables/connection_limiter.go.
const STRATEGIES: { value: ConnectionStrategy; label: string }[] = [
  { value: "connection", label: "Connection" },
  { value: "bypass", label: "Bypass" },
];
const CONN_TYPES: { value: ConnectionType; label: string }[] = [
  { value: "hwid", label: "HWID" },
  { value: "mux", label: "Mux" },
  { value: "source_ip", label: "Source IP" },
  { value: "default", label: "Default" },
];
const LOCK_TYPES: { value: LockType; label: string }[] = [
  { value: "manager", label: "Manager" },
  { value: "default", label: "Default" },
];

export function ConnectionLimitersPage() {
  const api = useApi();
  const squads = useSquadCatalog(api);
  // Memoise the CRUD config so CrudPage's `reload` callback stays stable
  // across re-renders. Recomputed when the API client flips or a new
  // squad name is merged into the catalog through observeRows.
  const config = useMemo<
    CrudConfig<ConnectionLimiter, ConnectionLimiterCreate, ConnectionLimiterUpdate>
  >(() => ({
    title: "Connection limiters",
    icon: <LinkIcon />,
    idKey: "id",
    onRowsChange: (rows) => squads.observeRows(rows, pickSquadIds),
    columns: [
      { key: "id", label: "ID" },
      {
        key: "squad_ids",
        label: "Squads",
        sortable: false,
        render: renderSquadIds<ConnectionLimiter>(squads.names),
      },
      { key: "username", label: "Username" },
      { key: "outbound", label: "Outbound" },
      { key: "strategy", label: "Strategy", render: renderOptionLabel<ConnectionLimiter>("strategy", STRATEGIES) },
      {
        key: "connection_type",
        label: "Connection type",
        render: renderOptionLabel<ConnectionLimiter>("connection_type", CONN_TYPES),
      },
      { key: "lock_type", label: "Lock type", render: renderOptionLabel<ConnectionLimiter>("lock_type", LOCK_TYPES) },
      {
        key: "count",
        label: "Count",
        // bypass disables the limiter server-side
        // (excluded_if=Strategy bypass on the DTO), so `count` arrives
        // as 0. Render an unambiguous ∞ instead so the column reads as
        // "no cap" at a glance instead of looking like a real zero.
        render: (row) => (row.strategy === "bypass" ? "∞" : row.count),
      },
      { key: "created_at", label: "Created at" },
      { key: "updated_at", label: "Updated at" },
    ],
    filters: [
      { name: "username", label: "Username", type: "text" },
      { name: "outbound", label: "Outbound", type: "text" },
      { name: "strategy", label: "Strategy", type: "select", options: STRATEGIES },
      { name: "connection_type", label: "Connection type", type: "select", options: CONN_TYPES },
      { name: "lock_type", label: "Lock type", type: "select", options: LOCK_TYPES },
      { name: "created_at", label: "Created at", type: "datetime-range" },
      { name: "updated_at", label: "Updated at", type: "datetime-range" },
    ],
    fields: [
      // Mirror service/admin_panel/tables/connection_limiter.go: squads,
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
        // (excluded_if=Strategy bypass on the DTO). Wipe their values when
        // switching so a stale entry can't be smuggled out of a hidden field.
        clears: ["connection_type", "lock_type", "count"],
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
        name: "lock_type",
        label: "Lock type",
        type: "select",
        required: true,
        options: LOCK_TYPES,
        visibleWhen: (form) => form.strategy !== "bypass",
      },
      {
        name: "count",
        label: "Count",
        type: "number",
        required: true,
        visibleWhen: (form) => form.strategy !== "bypass",
      },
    ],
    list: (q) => api.connectionLimiters.list(q),
    count: (q) => api.connectionLimiters.count(q),
    create: (b) => api.connectionLimiters.create(b),
    update: (id, b) => api.connectionLimiters.update(Number(id), b),
    remove: (id) => api.connectionLimiters.remove(Number(id)),
    fromEntity: (e) => ({
      username: e.username ?? "",
      outbound: e.outbound,
      strategy: e.strategy,
      connection_type: e.connection_type ?? "",
      lock_type: e.lock_type,
      count: e.count,
    }),
    // `connection_type` / `lock_type` / `count` carry
    // `excluded_if=Strategy bypass` on the manager-api DTO; the
    // validator rejects non-zero values when strategy=bypass and any
    // empty/zero values still get persisted by the SQL repo, so drop
    // the keys entirely on bypass (`undefined` → omitted by
    // `JSON.stringify`).
    toCreate: (f) => {
      const strategy = String(f.strategy ?? "") as ConnectionStrategy;
      const bypass = strategy === "bypass";
      return {
        squad_ids: parseSquadIds(f.squad_ids),
        username: f.username ? String(f.username) : undefined,
        outbound: String(f.outbound ?? "").trim(),
        strategy,
        connection_type: bypass
          ? undefined
          : f.connection_type
            ? (String(f.connection_type) as ConnectionType)
            : undefined,
        lock_type: bypass ? undefined : (String(f.lock_type ?? "") as LockType),
        count: bypass ? undefined : Number(f.count ?? 0),
      };
    },
    // username and outbound are locked on update, so reuse the original
    // entity's values to satisfy the API's required-field validation.
    toUpdate: (f, original) => {
      const strategy = String(f.strategy ?? "") as ConnectionStrategy;
      const bypass = strategy === "bypass";
      return {
        username: original.username || undefined,
        outbound: original.outbound,
        strategy,
        connection_type: bypass
          ? undefined
          : f.connection_type
            ? (String(f.connection_type) as ConnectionType)
            : undefined,
        lock_type: bypass ? undefined : (String(f.lock_type ?? "") as LockType),
        count: bypass ? undefined : Number(f.count ?? 0),
      };
    },
  }), [api, squads]);
  return (
    <CrudPage<ConnectionLimiter, ConnectionLimiterCreate, ConnectionLimiterUpdate> config={config} />
  );
}
