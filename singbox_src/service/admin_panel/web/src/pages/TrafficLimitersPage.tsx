import RestartAltIcon from "@mui/icons-material/RestartAlt";
import SwapHorizIcon from "@mui/icons-material/SwapHoriz";
import { Box, LinearProgress, Typography } from "@mui/material";
import { useMemo } from "react";
import {
  CrudPage,
  renderOptionLabel,
  type CrudConfig,
} from "../components/CrudPage";
import { useApi } from "../auth/AuthContext";
import { useNotify } from "../notifications/NotificationsProvider";
import type {
  TrafficLimiter,
  TrafficLimiterCreate,
  TrafficLimiterUpdate,
  TrafficMode,
  TrafficStrategy,
} from "../api/types";
import {
  parseSquadIds,
  pickSquadIds,
  renderSquadIds,
  squadIdsField,
  useSquadCatalog,
} from "./squadField";

// Display labels mirror service/admin_panel/tables/traffic_limiter.go.
const STRATEGIES: { value: TrafficStrategy; label: string }[] = [
  { value: "global", label: "Global" },
  { value: "bypass", label: "Bypass" },
];
const MODES: { value: TrafficMode; label: string }[] = [
  { value: "download", label: "Download" },
  { value: "upload", label: "Upload" },
  { value: "bidirectional", label: "Bidirectional" },
];

// renderUsage shows the server-computed `usage` percentage (0–100,
// floored in SQL) as a horizontal progress bar with a "P %" label next
// to it. Computing on the back end keeps the bar's colour and the
// number side-by-side: there's no risk of the FE rounding 99.6 → 100
// while the colour threshold still sees 99.6.
function renderUsage(row: TrafficLimiter) {
  const pct = Math.min(100, Math.max(0, Number(row.usage ?? 0)));
  // Hint at the danger zone — bar shifts from primary to warning to error
  // as the limiter approaches / exceeds its quota.
  const color = pct >= 100 ? "error" : pct >= 80 ? "warning" : "primary";
  return (
    // Capped-width wrapper: on the desktop table the bar always renders
    // at its 140 px max regardless of how wide the user has stretched
    // the "Usage" column (without the cap, the inner bar — which has
    // `flexGrow: 1` — would expand to fill the cell, making every
    // Usage column visibly different across viewports). On narrow
    // mobile cards the wrapper shrinks down to the cell's actual
    // width via `width: 100%` + `minWidth: 0`, so the bar stays on
    // the same row as its percentage label instead of overflowing.
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        flexWrap: "nowrap",
        gap: 1,
        width: "100%",
        maxWidth: 140,
        minWidth: 0,
      }}
    >
      <Typography
        variant="caption"
        sx={{
          color: "text.secondary",
          fontVariantNumeric: "tabular-nums",
          flexShrink: 0,
          whiteSpace: "nowrap",
          // Mobile cards apply `wordBreak: break-word` to value cells,
          // which would let the browser snap "100%" between digits and
          // the percent sign on a narrow viewport. Forcing the value to
          // stay on one glyph row keeps the percent and the bar on the
          // same line at every screen width.
          wordBreak: "keep-all",
          overflowWrap: "normal",
        }}
      >
        {pct}%
      </Typography>
      <Box sx={{ flexGrow: 1, flexShrink: 1, minWidth: 0 }}>
        <LinearProgress
          variant="determinate"
          value={pct}
          color={color}
          sx={{ height: 6, borderRadius: 3 }}
        />
      </Box>
    </Box>
  );
}

export function TrafficLimitersPage() {
  const api = useApi();
  const notify = useNotify();
  const squads = useSquadCatalog(api);
  // Memoise the CRUD config so CrudPage's `reload` callback stays stable
  // across re-renders. Recomputed when the API client flips or a new
  // squad name is merged into the catalog through observeRows.
  const config = useMemo<
    CrudConfig<TrafficLimiter, TrafficLimiterCreate, TrafficLimiterUpdate>
  >(() => ({
    title: "Traffic limiters",
    icon: <SwapHorizIcon />,
    idKey: "id",
    onRowsChange: (rows) => squads.observeRows(rows, pickSquadIds),
    // Reset traffic — wipes raw_used to 0 via the manager-api
    // PUT /traffic-limiters/{id}/used endpoint, then reloads the
    // table so the Usage bar snaps back to 0 %. Hidden for rows that
    // already have zero usage so the button doesn't read as a no-op.
    rowActions: [
      {
        key: "reset",
        label: "Reset traffic",
        icon: <RestartAltIcon fontSize="small" />,
        visible: (row) => Number(row.raw_used ?? 0) > 0,
        // Pop a styled MUI confirm dialog (same chrome as the Delete
        // dialog), not a browser-native window.confirm. The dialog is
        // tinted "warning" to flag the action without reading as
        // destructive — usage data is recoverable but the counter
        // mid-period isn't.
        confirm: (row) => ({
          title: `Reset traffic for limiter #${row.id}?`,
          description:
            "The used traffic counter will be set back to 0. The limiter's quota and configuration are unchanged.",
          confirmLabel: "Reset",
          busyLabel: "Resetting…",
          color: "warning",
        }),
        onClick: async (row, ctx) => {
          await api.trafficLimiters.updateUsed(row.id, 0);
          notify.success(`Traffic reset for limiter #${row.id}`);
          await ctx.reload();
        },
      },
    ],
    columns: [
      { key: "id", label: "ID" },
      {
        key: "squad_ids",
        label: "Squads",
        sortable: false,
        render: renderSquadIds<TrafficLimiter>(squads.names),
      },
      { key: "username", label: "Username" },
      { key: "outbound", label: "Outbound" },
      { key: "strategy", label: "Strategy", render: renderOptionLabel<TrafficLimiter>("strategy", STRATEGIES) },
      { key: "mode", label: "Mode", render: renderOptionLabel<TrafficLimiter>("mode", MODES) },
      { key: "usage", label: "Usage", render: renderUsage },
      {
        key: "quota",
        label: "Quota",
        // bypass disables the limiter's quota server-side
        // (excluded_if=Strategy bypass on the DTO), so the row's `quota`
        // arrives as an empty string. Render an unambiguous ∞ instead so
        // the column reads as "no cap" at a glance instead of looking
        // like missing data.
        render: (row) => (row.strategy === "bypass" ? "∞" : row.quota),
      },
      { key: "created_at", label: "Created at" },
      { key: "updated_at", label: "Updated at" },
    ],
    filters: [
      { name: "username", label: "Username", type: "text" },
      { name: "outbound", label: "Outbound", type: "text" },
      { name: "strategy", label: "Strategy", type: "select", options: STRATEGIES },
      { name: "mode", label: "Mode", type: "select", options: MODES },
      { name: "created_at", label: "Created at", type: "datetime-range" },
      { name: "updated_at", label: "Updated at", type: "datetime-range" },
    ],
    fields: [
      // Mirror service/admin_panel/tables/traffic_limiter.go: squads,
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
        clears: ["mode", "quota"],
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
        name: "quota",
        label: "Quota",
        type: "text",
        required: true,
        helperText: "e.g. 10gb",
        visibleWhen: (form) => form.strategy !== "bypass",
      },
    ],
    list: (q) => api.trafficLimiters.list(q),
    count: (q) => api.trafficLimiters.count(q),
    create: (b) => api.trafficLimiters.create(b),
    update: (id, b) => api.trafficLimiters.update(Number(id), b),
    remove: (id) => api.trafficLimiters.remove(Number(id)),
    fromEntity: (e) => ({
      username: e.username ?? "",
      outbound: e.outbound,
      strategy: e.strategy,
      mode: e.mode,
      quota: e.quota,
    }),
    // `mode` / `quota` carry `excluded_if=Strategy bypass` on the
    // manager-api DTO and the SQL repository unconditionally parses
    // `quota` via byteformats — sending empty strings with a bypass
    // payload would make the server reject the request with 400
    // "invalid format". Drop the keys entirely on bypass so
    // `JSON.stringify` skips them.
    toCreate: (f) => {
      const strategy = String(f.strategy ?? "") as TrafficStrategy;
      return {
        squad_ids: parseSquadIds(f.squad_ids),
        username: f.username ? String(f.username) : undefined,
        outbound: String(f.outbound ?? "").trim(),
        strategy,
        mode: strategy === "bypass" ? undefined : (String(f.mode ?? "") as TrafficMode),
        quota: strategy === "bypass" ? undefined : String(f.quota ?? "").trim(),
      };
    },
    toUpdate: (f, original) => {
      const strategy = String(f.strategy ?? "") as TrafficStrategy;
      return {
        username: original.username || undefined,
        outbound: original.outbound,
        strategy,
        mode: strategy === "bypass" ? undefined : (String(f.mode ?? "") as TrafficMode),
        quota: strategy === "bypass" ? undefined : String(f.quota ?? "").trim(),
      };
    },
  }), [api, notify, squads]);
  return <CrudPage<TrafficLimiter, TrafficLimiterCreate, TrafficLimiterUpdate> config={config} />;
}
