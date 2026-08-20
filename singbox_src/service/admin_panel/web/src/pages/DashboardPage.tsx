import {
  Box,
  Card,
  CardActionArea,
  CardContent,
  CircularProgress,
  Stack,
  Typography,
} from "@mui/material";
import Grid from "@mui/material/Grid2";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link as RouterLink } from "react-router-dom";
import DashboardIcon from "@mui/icons-material/Dashboard";
import GroupsIcon from "@mui/icons-material/Groups";
import StorageIcon from "@mui/icons-material/Storage";
import PeopleIcon from "@mui/icons-material/People";
import SpeedIcon from "@mui/icons-material/Speed";
import SwapHorizIcon from "@mui/icons-material/SwapHoriz";
import LinkIcon from "@mui/icons-material/Link";
import FilterAltIcon from "@mui/icons-material/FilterAlt";
import { useApi } from "../auth/AuthContext";
import { notifyApiError, useNotify } from "../notifications/NotificationsProvider";
import { PageHeader } from "../components/PageHeader";

interface Tile {
  label: string;
  to: string;
  icon: ReactNode;
  value: number | null;
}

export function DashboardPage() {
  const api = useApi();
  const notify = useNotify();
  // Every tile renders with `var(--sb-accent)` (see DashboardTile below),
  // so the order is the only thing that matters here — it mirrors the
  // navigation in <Layout/> and the table registration order in
  // service/admin_panel/service.go.
  const [tiles, setTiles] = useState<Tile[]>([
    { label: "Squads", to: "/squads", icon: <GroupsIcon />, value: null },
    { label: "Nodes", to: "/nodes", icon: <StorageIcon />, value: null },
    { label: "Users", to: "/users", icon: <PeopleIcon />, value: null },
    {
      label: "Connection limiters",
      to: "/connection-limiters",
      icon: <LinkIcon />,
      value: null,
    },
    {
      label: "Bandwidth limiters",
      to: "/bandwidth-limiters",
      icon: <SpeedIcon />,
      value: null,
    },
    {
      label: "Traffic limiters",
      to: "/traffic-limiters",
      icon: <SwapHorizIcon />,
      value: null,
    },
    {
      label: "Rate limiters",
      to: "/rate-limiters",
      icon: <FilterAltIcon />,
      value: null,
    },
  ]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        // Counts are fetched in the same positional order as the `tiles`
        // array above: Squads, Nodes, Users, Connection, Bandwidth, Traffic,
        // Rate.
        const values = await Promise.all([
          api.squads.count(),
          api.nodes.count(),
          api.users.count(),
          api.connectionLimiters.count(),
          api.bandwidthLimiters.count(),
          api.trafficLimiters.count(),
          api.rateLimiters.count(),
        ]);
        if (cancelled) return;
        setTiles((prev) => prev.map((tile, i) => ({ ...tile, value: values[i] ?? 0 })));
      } catch (e) {
        // Surface counter-load failures via the global toast stack
        // instead of an inline Alert above the tiles — the tiles
        // themselves keep their loading spinners (their `value` stays
        // null) so the page reads as "not loaded yet" without an
        // extra red bar competing with the rest of the dashboard
        // chrome.
        if (!cancelled) notifyApiError(notify, "Failed to load dashboard counters", e);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [api, notify]);

  return (
    <Box>
      <PageHeader
        icon={<DashboardIcon />}
        title="Dashboard"
      />
      <Grid container spacing={2}>
        {tiles.map((t, i) => (
          <Grid key={t.label} size={{ xs: 12, sm: 6, md: 4, lg: 3 }}>
            <DashboardTile tile={t} index={i} />
          </Grid>
        ))}
      </Grid>
    </Box>
  );
}

// DashboardTile renders a single counter card. `index` drives the
// per-tile delay on the entrance animation so the cards fade in as a
// staggered cascade rather than all at once.
//
// The entrance animation is driven by the Web Animations API (`element.animate`)
// rather than a CSS `@keyframes` rule for one specific reason: on a cold
// browser reload users reported the dashboard "drawing twice". Emotion
// re-serialises the `sx` block on every re-render, and although the
// resulting className is content-deterministic, certain edge cases —
// CSS bundle attaching after the JS commit, font swap forcing a class
// re-application, or `tile.value` going `null → number` causing the
// animation property to be re-applied — can re-trigger a CSS keyframe
// animation on an already-mounted element. WAAPI plus a `useRef` flag
// guarantees the entrance plays exactly once per fiber lifetime, no
// matter how many times React commits or how emotion shuffles classes
// underneath.
function DashboardTile({ tile, index }: { tile: Tile; index: number }) {
  // Every tile uses the global theme accent (`var(--sb-accent)`) so all
  // dashboard cards re-tint together when the user picks a new theme
  // colour. Translucent variants are produced via `color-mix` so they
  // automatically follow the variable too.
  const ACCENT = "var(--sb-accent)";
  const accentMix = (pct: number) =>
    `color-mix(in srgb, ${ACCENT} ${pct}%, transparent)`;
  const cardRef = useRef<HTMLDivElement | null>(null);
  const animatedRef = useRef(false);
  // Run as a layout effect so the keyframe is registered before the
  // browser paints the first frame; without `useLayoutEffect` the card
  // would briefly flash at full opacity before the WAAPI animation
  // captured the `from` state on the first paint after mount.
  useLayoutEffect(() => {
    const el = cardRef.current;
    if (!el || animatedRef.current) return;
    if (typeof el.animate !== "function") return;
    animatedRef.current = true;
    el.animate(
      [
        { opacity: 0, transform: "translateY(12px)" },
        { opacity: 1, transform: "translateY(0)" },
      ],
      {
        duration: 480,
        delay: index * 70,
        easing: "cubic-bezier(0.22, 0.61, 0.36, 1)",
        fill: "backwards",
      },
    );
  }, [index]);
  return (
    <Card
      ref={cardRef}
      sx={{
        height: "100%",
        position: "relative",
        overflow: "hidden",
        background: `linear-gradient(135deg, ${accentMix(4)} 0%, transparent 70%)`,
        transition:
          "transform 0.18s cubic-bezier(0.22, 0.61, 0.36, 1), border-color 0.18s, box-shadow 0.24s, background 0.32s",
        "&:hover": {
          borderColor: accentMix(55),
          transform: "translateY(-2px)",
          boxShadow: `0 12px 28px ${accentMix(18)}, 0 1px 0 ${accentMix(20)}`,
        },
        "&::before": {
          content: '""',
          position: "absolute",
          top: 0,
          left: 0,
          right: 0,
          height: 2,
          background: `linear-gradient(90deg, ${ACCENT} 0%, ${accentMix(40)} 100%)`,
          opacity: 0.85,
        },
      }}
    >
      <CardActionArea component={RouterLink} to={tile.to} sx={{ height: "100%" }}>
        <CardContent sx={{ p: 2.5 }}>
          <Stack direction="row" alignItems="center" spacing={2}>
            <Box
              sx={{
                width: 48,
                height: 48,
                borderRadius: 2.5,
                display: "grid",
                placeItems: "center",
                bgcolor: accentMix(16),
                color: ACCENT,
                border: `1px solid ${accentMix(30)}`,
                boxShadow: `inset 0 0 0 1px ${accentMix(6)}`,
                transition:
                  "background-color 0.32s cubic-bezier(0.4,0,0.2,1), color 0.32s cubic-bezier(0.4,0,0.2,1), border-color 0.32s cubic-bezier(0.4,0,0.2,1)",
              }}
            >
              {tile.icon}
            </Box>
            <Box sx={{ flexGrow: 1, minWidth: 0 }}>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{
                  textTransform: "uppercase",
                  letterSpacing: 1.2,
                  fontWeight: 600,
                  fontSize: 10.5,
                  display: "block",
                }}
              >
                {tile.label}
              </Typography>
              {/* Reserve a fixed-height row for the value so swapping
                  the loading spinner for the final number doesn't move
                  the card by ~10 px when the count fetch resolves. The
                  height matches the line-box of the 32 px number text
                  (font-size × line-height = 32 × 1 = 32). The spinner
                  is centred inside this row instead of forcing its own
                  smaller intrinsic height onto the parent column,
                  which is what made the dashboard read as "animating
                  twice" on a cold browser reload — the tile slid in
                  via `tileIn`, then jumped down a row when the
                  spinner was replaced by the larger number. */}
              <Box
                sx={{
                  mt: 0.75,
                  height: 32,
                  display: "flex",
                  alignItems: "center",
                }}
              >
                {tile.value === null ? (
                  <CircularProgress size={22} thickness={5} sx={{ color: ACCENT }} />
                ) : (
                  <Typography
                    variant="h3"
                    sx={{
                      lineHeight: 1,
                      fontSize: 32,
                      fontWeight: 600,
                      letterSpacing: -0.6,
                      fontVariantNumeric: "tabular-nums",
                    }}
                  >
                    {tile.value}
                  </Typography>
                )}
              </Box>
            </Box>
          </Stack>
        </CardContent>
      </CardActionArea>
    </Card>
  );
}
