import {
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  ListSubheader,
  Popover,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme,
} from "@mui/material";
import LogoutIcon from "@mui/icons-material/Logout";
import LightModeIcon from "@mui/icons-material/LightMode";
import DarkModeIcon from "@mui/icons-material/DarkMode";
import DashboardIcon from "@mui/icons-material/Dashboard";
import GroupsIcon from "@mui/icons-material/Groups";
import StorageIcon from "@mui/icons-material/Storage";
import PeopleIcon from "@mui/icons-material/People";
import LinkIcon from "@mui/icons-material/Link";
import SpeedIcon from "@mui/icons-material/Speed";
import SwapHorizIcon from "@mui/icons-material/SwapHoriz";
import FilterAltIcon from "@mui/icons-material/FilterAlt";
import GitHubIcon from "@mui/icons-material/GitHub";
import FavoriteIcon from "@mui/icons-material/Favorite";
import ChevronLeftIcon from "@mui/icons-material/ChevronLeft";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import MenuIcon from "@mui/icons-material/Menu";
import InfoIcon from "@mui/icons-material/Info";
import brandIcon from "../assets/icon.svg";
import { Link as RouterLink, useLocation } from "react-router-dom";
import { useEffect, useState, type ReactNode, type MouseEvent } from "react";
import { useApi, useAuth } from "../auth/AuthContext";
import type { VersionInfo } from "../api/types";
import { useAccent } from "../theme/AppThemeProvider";
import { ColorPickerButton } from "./ColorPickerButton";

const leftDrawerWidthExpanded = 240;
const leftDrawerWidthCollapsed = 56;
const headerHeight = 60;
const navItemSize = 40; // square size of each menu button when collapsed
// Smooth, snappy animation for the sidebar collapse/expand.
const drawerTransition = "width 0.22s cubic-bezier(0.4, 0, 0.2, 1)";

const COLLAPSED_KEY = "sing-box-admin:nav-collapsed";

// NavItem is one of three flavours, mutually exclusive:
//   - `to`     — internal route, navigated via react-router; the item
//                lights up as "selected" when the URL matches.
//   - `href`   — external URL; opens in a new tab, never selected.
//   - `action` — ad-hoc onClick (e.g. opens a popover); never
//                selected, doesn't dismiss the mobile drawer because
//                the click target is meant to remain anchored.
interface NavItem {
  label: string;
  icon: ReactNode;
  to?: string;
  href?: string;
  action?: (e: MouseEvent<HTMLElement>) => void;
}

interface NavGroup {
  header?: string;
  items: NavItem[];
}

const NAV_GROUPS: NavGroup[] = [
  {
    header: "General",
    items: [
      { to: "/", label: "Dashboard", icon: <DashboardIcon fontSize="small" /> },
      { to: "/squads", label: "Squads", icon: <GroupsIcon fontSize="small" /> },
      { to: "/nodes", label: "Nodes", icon: <StorageIcon fontSize="small" /> },
      { to: "/users", label: "Users", icon: <PeopleIcon fontSize="small" /> },
    ],
  },
  {
    header: "Limiters",
    items: [
      // Order matches the table-registration order in service/admin_panel:
      // connection → bandwidth → traffic → rate.
      {
        to: "/connection-limiters",
        label: "Connection limiters",
        icon: <LinkIcon fontSize="small" />,
      },
      {
        to: "/bandwidth-limiters",
        label: "Bandwidth limiters",
        icon: <SpeedIcon fontSize="small" />,
      },
      {
        to: "/traffic-limiters",
        label: "Traffic limiters",
        icon: <SwapHorizIcon fontSize="small" />,
      },
      {
        to: "/rate-limiters",
        label: "Rate limiters",
        icon: <FilterAltIcon fontSize="small" />,
      },
    ],
  },
  {
    // Mirrors the "Miscellaneous" menu entries from
    // service/admin_panel/migration/postgresql.go.
    header: "Miscellaneous",
    items: [
      {
        href: "https://github.com/shtorm-7/sing-box-extended",
        label: "GitHub",
        icon: <GitHubIcon fontSize="small" />,
      },
      {
        href: "https://github.com/shtorm-7/sing-box-extended#support-the-project",
        label: "Donate",
        icon: <FavoriteIcon fontSize="small" />,
      },
    ],
  },
];

export function Layout({ children }: { children: ReactNode }) {
  const { logout } = useAuth();
  const api = useApi();
  const location = useLocation();
  const { palette, mode, toggleMode } = useAccent();
  const theme = useTheme();
  // Version info shown in the bottom-of-sidebar About popover.
  //
  // Two loads on mount, each from a different service — the panel
  // is reachable via two HTTP endpoints, and we surface both so a
  // mismatch is visible at a glance:
  //
  //   - "Server" — `versionInfo` from manager-api's authenticated
  //     `/version` (fetched through the API client).
  //   - "Web"    — `webVersion` from admin_panel's own
  //     `/version`, which serves `constant.Version` of the running
  //     sing-box binary (the same string the Makefile injects via
  //     `-ldflags -X` at link time). Fetched with a relative URL so
  //     a sub-path deployment (admin panel behind a reverse proxy)
  //     keeps working without configuration.
  //
  // Both loads are fire-and-forget; failures fall through to a
  // placeholder rather than disrupting the layout — the sidebar
  // should never break because of a /version response.
  const [versionInfo, setVersionInfo] = useState<VersionInfo | null>(null);
  const [webVersion, setWebVersion] = useState<string | null>(null);
  const [aboutAnchor, setAboutAnchor] = useState<HTMLElement | null>(null);
  // Sign-out confirmation: the bottom-bar Logout button used to call
  // `logout` directly, which made it trivial to drop the session by
  // accident (the icon sits one row below the colour-picker / theme
  // toggle that users poke at constantly). Gate it behind a confirm
  // dialog instead — same shape as the delete-confirmation dialog in
  // CrudPage so the two reads as a coherent pair.
  const [signOutOpen, setSignOutOpen] = useState(false);
  useEffect(() => {
    let cancelled = false;
    api
      .version()
      .then((info) => {
        if (!cancelled) setVersionInfo(info);
      })
      .catch(() => {
        /* keep `null`; popover renders a placeholder. */
      });
    // The web version comes from the admin_panel service that
    // served us, NOT manager-api. `./version` resolves relative to
    // the document URL so a sub-path deployment still hits the
    // right origin without us having to compute a base URL.
    fetch("./version", { cache: "no-store" })
      .then((r) => (r.ok ? r.json() : null))
      .then((data: { version?: string } | null) => {
        if (!cancelled && data && typeof data.version === "string") {
          setWebVersion(data.version);
        }
      })
      .catch(() => {
        /* keep `null`; popover renders a placeholder. */
      });
    return () => {
      cancelled = true;
    };
  }, [api]);
  const openAbout = (e: MouseEvent<HTMLElement>) => setAboutAnchor(e.currentTarget);
  const closeAbout = () => setAboutAnchor(null);
  // Augment the static NAV_GROUPS with the About entry as the last
  // item of "Miscellaneous". Done here (rather than at module scope)
  // because the action callback closes over `openAbout`, which only
  // exists once the component is rendering.
  const navGroups: NavGroup[] = NAV_GROUPS.map((group) =>
    group.header === "Miscellaneous"
      ? {
          ...group,
          items: [
            ...group.items,
            {
              label: "About",
              icon: <InfoIcon fontSize="small" />,
              action: openAbout,
            },
          ],
        }
      : group,
  );
  // Single breakpoint governs the "mobile" treatment: below `md`
  // (< 900 px) the permanent sidebar becomes a temporary drawer
  // opened by a hamburger. Portrait tablets + phones all fall under
  // this threshold; desktop (≥ 900 px) keeps the original collapsible
  // sidebar layout. Topbar buttons (theme toggle, accent picker,
  // sign-out) are icon-only at every viewport so the row's geometry
  // doesn't shift across the breakpoint.
  const isMobile = useMediaQuery(theme.breakpoints.down("md"));
  const [mobileOpen, setMobileOpen] = useState(false);
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(COLLAPSED_KEY) === "1";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem(COLLAPSED_KEY, collapsed ? "1" : "0");
    } catch {
      /* ignore — write may fail in private-mode / quota-exceeded; the
         in-memory state still holds for the session. */
    }
  }, [collapsed]);

  // Auto-close the mobile drawer whenever the viewport crosses back
  // over the breakpoint — otherwise `mobileOpen` could stay `true` and
  // leave the desktop layout with an invisible Modal backdrop blocking
  // clicks on the main content.
  useEffect(() => {
    if (!isMobile && mobileOpen) setMobileOpen(false);
  }, [isMobile, mobileOpen]);

  // Collapse/expand is a desktop-only affordance. On mobile the drawer
  // is either shown in full (mobileOpen = true) or hidden — we never
  // render it at the 56 px collapsed width.
  const effectiveCollapsed = isMobile ? false : collapsed;
  const drawerWidth = effectiveCollapsed
    ? leftDrawerWidthCollapsed
    : leftDrawerWidthExpanded;

  // Shared nav content (brand + grouped items) is rendered inside both
  // the permanent and the temporary drawer so the two variants don't
  // have to duplicate markup. `effectiveCollapsed` drives whether the
  // brand caption + item labels are visible.
  const drawerContent = (
    <>
        <Box
          sx={{
            // Same triple-height-lock as the topbar in the main column:
            // explicit `height` + matching min/max + `flexShrink: 0`
            // makes the brand strip a fixed-size brick so the sliding
            // sidebar's content never reflows the brand row when the
            // nav list grows past the drawer's available height. The
            // nav list below this Box already has its own `overflow:
            // auto`, so any leftover items scroll inside it instead
            // of compressing the brand strip out from underneath.
            height: headerHeight,
            minHeight: headerHeight,
            maxHeight: headerHeight,
            flexShrink: 0,
            boxSizing: "border-box",
            position: "relative",
          }}
        >
          {/* Single brand block: icon + wordmark grouped inside one
              absolutely-positioned flex row. The wrapper's left edge
              is a literal pixel value, its width is content-based,
              and its inner flex layout is static (no values that
              change between collapsed / expanded), so the whole
              group sits at a fixed position relative to the drawer
              paper. When the drawer's `width` animates, the brand
              block doesn't move, recompute, or jitter — the
              drawer's `overflowX: hidden` simply clips off the
              trailing edge of the wordmark as the paper narrows. */}
          <Box
            sx={{
              position: "absolute",
              left: `${(leftDrawerWidthCollapsed - 32) / 2}px`,
              top: 0,
              bottom: 0,
              display: "flex",
              alignItems: "center",
              gap: 1.5,
            }}
          >
            <Box
              component="img"
              src={brandIcon}
              alt="sing-box"
              sx={{
                width: 32,
                height: 32,
                flexShrink: 0,
                display: "block",
                objectFit: "contain",
              }}
            />
            <Box
              sx={{
                // Only `opacity` is animated. Opacity can't perturb
                // layout, so the wordmark's glyphs never shift.
                opacity: effectiveCollapsed ? 0 : 1,
                transition: "opacity 0.18s ease",
                whiteSpace: "nowrap",
                // Stop hovering / focusing the fading text from
                // getting in the way when the drawer is collapsed.
                pointerEvents: effectiveCollapsed ? "none" : "auto",
              }}
            >
            {/* Wordmark — solid text-primary. We previously layered an
                accent-tinted gradient on top via `background-clip: text`,
                but the gradient's bottom edge sat right above the
                accent-coloured "extended" subtitle, and the two
                accent-tinted bands fused into a soft halo that read as
                a glow around the lower row. Dropping the gradient
                removes that fused band entirely while leaving the
                accent visible only where it was always intended — on
                "extended" itself.

                Both rows are kept as block-level boxes with explicit
                heights so the `g` descender stays inside the wordmark's
                row and never spills onto the subtitle. */}
            <Box
              component="span"
              sx={(t) => ({
                display: "block",
                margin: 0,
                height: 22,
                fontFamily: t.typography.fontFamily,
                fontWeight: 700,
                fontSize: 17,
                letterSpacing: -0.45,
                lineHeight: "22px",
                color: t.palette.text.primary,
              })}
            >
              Sing-box
            </Box>
            {/* "EXTENDED" subtitle — a plain accent-coloured wordmark
                rather than a chip-style pill. The previous boxed
                version looked off-balance because a 1 px border around
                ~9 px uppercase text reads as too heavy at this small
                a font size. A clean text label keeps the brand quiet
                and lets the wordmark above carry the visual weight. */}
            <Box
              component="span"
              sx={{
                display: "block",
                margin: 0,
                fontSize: 10,
                fontWeight: 700,
                letterSpacing: 1.8,
                lineHeight: "12px",
                marginTop: "2px",
                textTransform: "uppercase",
                color: "var(--sb-accent)",
                transition: "color 0.32s cubic-bezier(0.4,0,0.2,1)",
              }}
            >
              extended
            </Box>
            </Box>
          </Box>
        </Box>
        <Box sx={{ overflow: "auto", overflowX: "hidden", flexGrow: 1, py: 1 }}>
          {navGroups.map((group, idx) => (
            <List
              key={group.header ?? `group-${idx}`}
              dense
              disablePadding
              // No inter-group spacing when collapsed — without their headers
              // the groups should read as a single column of icons.
              sx={{ mb: effectiveCollapsed ? 0 : 1 }}
              subheader={
                group.header ? (
                  // Pure-CSS show/hide: when the sidebar collapses we just
                  // squeeze the subheader's height to 0 and drop its
                  // opacity. No JS animation runs per frame — that's what
                  // was causing the freeze with three subheader Collapses
                  // animating simultaneously alongside the drawer-width
                  // CSS transition.
                  <ListSubheader
                    disableSticky
                    sx={{
                      bgcolor: "transparent",
                      color: "text.secondary",
                      fontSize: 10.5,
                      letterSpacing: 1.4,
                      lineHeight: effectiveCollapsed ? "0px" : "30px",
                      height: effectiveCollapsed ? 0 : "30px",
                      opacity: effectiveCollapsed ? 0 : 1,
                      overflow: "hidden",
                      textTransform: "uppercase",
                      // Align horizontally with the *visible* icon glyph.
                      // The icon column starts at 12 px from the drawer's
                      // left edge (8 px marginInline + 4 px pl on each
                      // ListItemButton) and is 32 px wide; small icons
                      // (~20 px) are centred inside that column, so their
                      // visible left edge sits at 12 + (32 − 20) / 2 = 18.
                      pl: "18px",
                      pr: 1,
                      transition:
                        "height 0.18s ease, opacity 0.18s ease, line-height 0.18s ease",
                    }}
                  >
                    {group.header}
                  </ListSubheader>
                ) : undefined
              }
            >
              {group.items.map((item) => {
                // External links + action items never highlight as
                // "selected" — only internal routes do.
                const selected = item.to
                  ? item.to === "/"
                    ? location.pathname === "/"
                    : location.pathname.startsWith(item.to)
                  : false;
                // Three flavours of nav item — pick the right element /
                // event wiring per type:
                //   - `action`: plain ListItemButton (renders a div/
                //     button) with onClick fired straight at the
                //     callback.
                //   - `href`:   anchor opening in a new tab.
                //   - `to`:     react-router internal navigation.
                const linkProps = item.action
                  ? { onClick: item.action }
                  : item.href
                    ? {
                        component: "a" as const,
                        href: item.href,
                        target: "_blank",
                        rel: "noopener noreferrer",
                      }
                    : {
                        component: RouterLink,
                        to: item.to!,
                      };
                const button = (
                  <ListItemButton
                    {...linkProps}
                    selected={selected}
                    sx={{
                      // The theme gives every ListItemButton marginInline: 8,
                      // so the button's outer width is `drawerWidth − 16`.
                      // With a 56 px collapsed drawer this leaves a 40 px
                      // button wide; horizontal padding splits the leftover
                      // 8 px around the 32 px icon column, keeping the icon
                      // centered both when collapsed and when expanded.
                      pl: "4px",
                      pr: "4px",
                      // No vertical margin: spacing between items is provided
                      // by the wrapper <Box> below via padding. Padding
                      // doesn't collapse, so the gap stays constant whether
                      // a Collapse-wrapped subheader is mounted or not —
                      // killing the small "freeze" that used to happen when
                      // the subheader unmounted at the end of the close
                      // animation.
                      my: 0,
                      // Fixed height equal to the collapsed button width so
                      // the button is a 48 × 48 square when collapsed and
                      // exactly the same height (just wider) when expanded.
                      // Replaces the previous `aspect-ratio` toggle, which
                      // was the source of the bounce on the expand/collapse
                      // transition.
                      minHeight: navItemSize,
                      height: navItemSize,
                      justifyContent: "flex-start",
                    }}
                  >
                    <ListItemIcon
                      sx={{
                        width: 32,
                        minWidth: 32,
                        justifyContent: "center",
                        mr: 1.5,
                      }}
                    >
                      {item.icon}
                    </ListItemIcon>
                    <ListItemText
                      primary={item.label}
                      primaryTypographyProps={{ fontSize: 15, noWrap: true }}
                      sx={{
                        opacity: effectiveCollapsed ? 0 : 1,
                        maxWidth: effectiveCollapsed ? 0 : 1000,
                        transition: "opacity 0.18s ease, max-width 0.22s ease",
                        overflow: "hidden",
                      }}
                    />
                  </ListItemButton>
                );
                // Each item lives in a wrapping Box that supplies the
                // vertical spacing via *padding*. Padding doesn't collapse,
                // so adjacent items always have an 8 px gap (4 + 4) whether
                // a Collapse-wrapped subheader sits between groups or is in
                // the middle of unmounting.
                //
                // The Tooltip is rendered unconditionally; toggling
                // `collapsed` only flips its title + listener flags. That
                // way React keeps the same DOM tree across collapse/expand
                // and doesn't unmount + remount every ListItemButton on
                // every toggle — which was causing a visible freeze with
                // ~10 menu items.
                return (
                  <Box
                    key={item.to ?? item.href ?? item.label}
                    sx={{ py: 0.5 }}
                    // Closing the mobile drawer on nav-item click is what
                    // makes the temporary drawer feel native — users
                    // don't have to dismiss it manually after picking a
                    // destination. External `href` items don't navigate
                    // in-app, but we still dismiss the drawer so the
                    // main layout isn't left with an open backdrop.
                    //
                    // Action items (e.g. About) are the exception: they
                    // open a popover anchored to the ListItemButton, so
                    // dismissing the drawer would yank the anchor off-
                    // screen. Skip the close for those — the user can
                    // tap the backdrop or hamburger to dismiss.
                    onClick={
                      isMobile && !item.action
                        ? () => setMobileOpen(false)
                        : undefined
                    }
                  >
                    <Tooltip
                      title={effectiveCollapsed ? item.label : ""}
                      placement="right"
                      disableHoverListener={!effectiveCollapsed}
                      disableFocusListener={!effectiveCollapsed}
                      disableTouchListener={!effectiveCollapsed}
                    >
                      {button}
                    </Tooltip>
                  </Box>
                );
              })}
            </List>
          ))}
        </Box>
    </>
  );

  return (
    // On desktop (md+) the root is locked to exactly the viewport
    // height so the page-content column can flex-fill it and any
    // CrudPage table card inside can claim "viewport − topbar"
    // height without page-level scroll. Mobile keeps `minHeight:
    // 100vh` so long card lists scroll naturally on phones.
    //
    // `overflow: hidden` on md+ is the safety net that keeps the
    // sticky topbar pinned to the viewport top no matter what
    // internal flex calculation goes on. Without it, any one-frame
    // overflow (a row mounting at its full content height before
    // its `flex: 1` parent has settled, an emotion class swap,
    // a font-metrics shift) lets the body become scrollable, and
    // a `position: sticky` topbar inside a once-scrollable body is
    // free to ride up *with* the scroll instead of clamping at
    // top: 0 — which is exactly what users were seeing as "topbar
    // and PageHeader jump higher right when the rows arrive". The
    // sole legitimate scroll on desktop is the table's own
    // `TableContainer` (`overflow-y: auto`), so clipping the root
    // is harmless: there is nothing the user could ever want to
    // scroll *outside* the table card.
    <Box
      sx={{
        display: "flex",
        height: { md: "100vh" },
        minHeight: "100vh",
        overflow: { md: "hidden" },
        bgcolor: "background.default",
      }}
    >
      {/* Left bar: on desktop this is a permanent, collapsible sidebar.
          On mobile it turns into a temporary drawer that slides in from
          the left, dismissed by tapping the backdrop or any nav item. */}
      {isMobile ? (
        <Drawer
          anchor="left"
          variant="temporary"
          open={mobileOpen}
          onClose={() => setMobileOpen(false)}
          // Keep the drawer mounted so opening it doesn't cost a tree
          // rebuild on every tap — the brand + nav items animate in
          // from the left instead of re-rendering.
          ModalProps={{ keepMounted: true }}
          sx={{
            [`& .MuiDrawer-paper`]: {
              width: leftDrawerWidthExpanded,
              boxSizing: "border-box",
              backgroundColor: palette.elevated,
              overflowX: "hidden",
            },
          }}
        >
          {drawerContent}
        </Drawer>
      ) : (
        <Drawer
          anchor="left"
          variant="permanent"
          sx={{
            width: drawerWidth,
            flexShrink: 0,
            transition: drawerTransition,
            [`& .MuiDrawer-paper`]: {
              width: drawerWidth,
              boxSizing: "border-box",
              backgroundColor: palette.elevated,
              overflowX: "hidden",
              transition: drawerTransition,
              // Hint the compositor that the drawer's width animates so the
              // browser can lift the paper to its own layer and avoid a full
              // reflow of every nav item on every animation frame.
              willChange: "width",
            },
          }}
        >
          {drawerContent}
        </Drawer>
      )}

      {/* Main column: thin top toolbar + page content. `minHeight: 0`
          (desktop) lets the column shrink below its natural content
          height so a fixed-height root + a flex-fill child can co-exist
          without overflowing the viewport — that's what enables the
          CrudPage table card to claim the leftover vertical space and
          scroll its rows internally. */}
      <Box
        component="main"
        sx={{
          flexGrow: 1,
          display: "flex",
          flexDirection: "column",
          minWidth: 0,
          minHeight: { md: 0 },
        }}
      >
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 1.5,
            // Mirror the responsive horizontal padding used by the
            // page-content Box below (`px: { xs: 1.5, md: 2 }`). With
            // a flat `px: 2` here the topbar's leading IconButton
            // (hamburger on mobile, chevron on desktop) sat 4 px
            // further to the right than the PageHeader icon badge
            // on phones — the two icons share an "icon column" by
            // design and need the same starting offset to actually
            // line up vertically.
            px: { xs: 1.5, md: 2 },
            // Triple-locked height: explicit `height` + matching
            // `min-height` / `max-height` make sure no descendant
            // mounting at an unexpected natural height (e.g. an
            // accent-color picker briefly painting taller during
            // its enter transition, an icon button growing a
            // halo) and no flex-shrink pressure from the
            // surrounding column can ever resize the topbar.
            // `flexShrink: 0` belt-and-braces the same guarantee
            // against the flex algorithm itself: even if the
            // main column's content overflows the viewport for
            // one frame, the topbar is treated as a fixed-size
            // brick the algorithm is not allowed to compress.
            height: headerHeight,
            minHeight: headerHeight,
            maxHeight: headerHeight,
            flexShrink: 0,
            boxSizing: "border-box",
            position: "sticky",
            top: 0,
            // Topbar has to paint above any sticky element rendered
            // inside the page content — most importantly CrudPage's
            // table, whose `stickyHeader` cells sit at z-index 2 and
            // whose pinned Actions column tops out at z-index 3. The
            // page-content Box wrapping `{children}` is `position:
            // relative` without a `z-index`, so it does NOT create a
            // stacking context: those table z-indices live in the
            // main-column stacking context alongside the topbar.
            //
            // On mobile / tablet (xs–sm) the body itself is the
            // scrolling container — `Layout`'s `height: { md: "100vh" }`
            // only kicks in at md+, where instead the table scrolls
            // its rows internally inside a `TableContainer` and the
            // sticky table cells stay clipped to that scroll viewport.
            // Below md, the table's sticky cells try to glue to the
            // same `top: 0` as the topbar, and the higher z-indices
            // would let them paint over the topbar (visually + for
            // hit-testing). Concretely: the pinned Actions column is
            // sticky to the *right* edge, so on a tablet a tap on
            // the rightmost topbar buttons (Sign out / colour picker
            // / theme toggle) used to fire the table's Edit/Delete
            // IconButton sitting underneath instead.
            //
            // `theme.zIndex.appBar` (1100) is the standard MUI layer
            // for top-level chrome — above any in-page sticky
            // content but still below the temporary mobile drawer
            // (`zIndex.drawer` = 1200), so opening the hamburger
            // still slides the drawer over the topbar the way it
            // always has.
            zIndex: theme.zIndex.appBar,
            backgroundColor: "background.default",
          }}
        >
          {/* Leading button: chevron (collapse/expand) on desktop,
              hamburger (open mobile drawer) on mobile. Sized at 46 × 46
              on desktop so it visually shares a column with the
              PageHeader icon badge; mobile uses the same size for a
              generous tap target. */}
          <IconButton
            onClick={
              isMobile
                ? () => setMobileOpen(true)
                : () => setCollapsed((c) => !c)
            }
            size="medium"
            aria-label={
              isMobile
                ? "Open navigation"
                : collapsed
                  ? "Show menu"
                  : "Hide menu"
            }
            sx={{
              color: "text.primary",
              borderRadius: 2.5,
              width: 46,
              height: 46,
              "&:hover": { backgroundColor: "action.hover" },
            }}
          >
            {isMobile ? (
              <MenuIcon />
            ) : (
              // On desktop the same button toggles the sidebar between
              // expanded (`ChevronLeftIcon`) and collapsed
              // (`ChevronRightIcon`) states. The two glyphs are mirror
              // images of each other but render with subtly different
              // optical centres — swapping them via a conditional
              // render shifted the icon a couple of pixels left/right
              // each click. Both icons are now stacked in the same
              // 24×24 box with `position: absolute` and only their
              // opacity flips, so the visual centre stays put no
              // matter which direction the chevron is pointing.
              <Box
                component="span"
                sx={{
                  position: "relative",
                  display: "inline-block",
                  width: 24,
                  height: 24,
                  "& > svg": {
                    position: "absolute",
                    top: 0,
                    left: 0,
                    fontSize: 24,
                  },
                }}
              >
                <ChevronLeftIcon sx={{ opacity: collapsed ? 0 : 1 }} />
                <ChevronRightIcon sx={{ opacity: collapsed ? 1 : 0 }} />
              </Box>
            )}
          </IconButton>
          <Box sx={{ flexGrow: 1 }} />
          <IconButton
            onClick={(e) => toggleMode(e)}
            size="medium"
            aria-label="Toggle color theme"
            sx={{
              color: "text.primary",
              borderRadius: 2,
              width: 40,
              height: 40,
              "&:hover": { backgroundColor: "action.hover" },
            }}
          >
            {mode === "light" ? (
              <DarkModeIcon fontSize="small" />
            ) : (
              <LightModeIcon fontSize="small" />
            )}
          </IconButton>
          <ColorPickerButton />
          {/* Sign out is icon-only at every viewport — same treatment
              the theme/colour-picker buttons get sitting next to it.
              Tooltip carries the label for hover/focus, `aria-label`
              keeps the action discoverable for screen readers. */}
          <Tooltip title="Sign out">
            <IconButton
              onClick={() => setSignOutOpen(true)}
              size="medium"
              aria-label="Sign out"
              sx={{
                color: "text.primary",
                borderRadius: 2,
                width: 40,
                height: 40,
                "&:hover": { backgroundColor: "action.hover" },
              }}
            >
              <LogoutIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Box>
        {/* Page-content slot. On desktop the box itself is a flex
            column so a child like CrudPage can `flex: 1` and fill the
            leftover vertical space below the topbar; on mobile the box
            falls back to block layout so dashboards / phone-friendly
            card lists scroll the page naturally. `minHeight: 0` is
            required for the flex-fill child to be allowed to shrink
            below its content height (so the table card scrolls its
            rows internally instead of pushing the topbar off-screen). */}
        <Box
          sx={{
            flexGrow: 1,
            px: { xs: 1.5, md: 2 },
            pb: 3,
            position: "relative",
            display: { md: "flex" },
            flexDirection: { md: "column" },
            minHeight: { md: 0 },
          }}
        >
          {children}
        </Box>
      </Box>
      {/* About popover — anchored to the sidebar Info button. Mounted
          at the layout root (outside the drawer paper) so the popover
          can escape the drawer's `overflowX: hidden` clip without us
          having to relax that for the rest of the sidebar. */}
      <Popover
        open={Boolean(aboutAnchor)}
        anchorEl={aboutAnchor}
        onClose={closeAbout}
        // Anchor on the right edge of the About row, vertically
        // centred — so the popover unfolds out to the side, sharing
        // the same baseline as the menu item instead of floating up
        // above it. Mirrors the placement of the collapsed-sidebar
        // tooltip so the popover reads as a slightly heavier version
        // of the same affordance.
        anchorOrigin={{ vertical: "center", horizontal: "right" }}
        transformOrigin={{ vertical: "center", horizontal: "left" }}
        slotProps={{
          paper: {
            sx: {
              ml: 1,
              p: 2,
              minWidth: 240,
              border: "1px solid",
              borderColor: "divider",
            },
          },
        }}
      >
        <Typography
          variant="overline"
          sx={{
            display: "block",
            color: "text.secondary",
            letterSpacing: 1.4,
            lineHeight: 1.4,
            mb: 1,
          }}
        >
          Sing-box Extended
        </Typography>
        {/* Two-row version table: server (running sing-box build,
            fetched from /version) and web (this SPA bundle's own
            version, baked in at build time). Labels in regular text,
            values in monospace + word-break so a long build hash
            wraps gracefully instead of pushing the popover wider. */}
        {[
          { label: "Server", value: versionInfo?.version ?? "loading…" },
          { label: "Web", value: webVersion ?? "loading…" },
        ].map((row) => (
          <Box
            key={row.label}
            sx={{
              display: "flex",
              alignItems: "baseline",
              gap: 1.5,
              "& + &": { mt: 0.5 },
            }}
          >
            <Typography
              sx={{
                fontSize: 12,
                color: "text.secondary",
                minWidth: 50,
              }}
            >
              {row.label}
            </Typography>
            <Typography
              sx={{
                fontFamily:
                  '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace',
                fontSize: 13,
                color: "text.primary",
                wordBreak: "break-all",
              }}
            >
              {row.value}
            </Typography>
          </Box>
        ))}
      </Popover>
      {/* Sign-out confirmation. Mirrors the delete-confirmation dialog
          in CrudPage (compact xs Dialog, body2 explanatory copy,
          Cancel + danger-coloured primary action) so the two read as
          a single style of "are you sure?" prompt across the app. */}
      <Dialog
        open={signOutOpen}
        onClose={() => setSignOutOpen(false)}
        maxWidth="xs"
        fullWidth
      >
        <DialogTitle>Sign out?</DialogTitle>
        <DialogContent dividers>
          <Typography variant="body2" color="text.secondary">
            You will be returned to the login screen and will need to
            sign in again to continue.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSignOutOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            color="error"
            startIcon={<LogoutIcon fontSize="small" />}
            onClick={() => {
              setSignOutOpen(false);
              logout();
            }}
          >
            Sign out
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
