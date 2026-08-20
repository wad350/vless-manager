import Fade from "@mui/material/Fade";
import { alpha, createTheme, type Theme } from "@mui/material/styles";

// Two layered palettes, one per mode. Surface and elevated tones differ
// by ~5 luminance steps so the eye can pick out cards/dialogs from the
// page; the rest of the UI references this set so flipping `mode` flips
// every component override at once.
export type ThemeMode = "dark" | "light";

const DARK = {
  surface: "#141414", // page / cards / paper / tables
  elevated: "#191919", // app bar replacement, table heads, side bar
  elevated2: "#1f1f1f", // hover states for table rows
  // Borders bumped a touch (0.06 → 0.10 / 0.14 → 0.20) so outlined
  // cards and table cell borders are actually visible against the
  // very dark surface.
  border: "rgba(255,255,255,0.10)",
  borderStrong: "rgba(255,255,255,0.20)",
  textPrimary: "#f5f5f5",
  // Secondary / caption text was washing out at 0.66 — bumped to 0.80
  // so subtitles, helper text and uppercase labels stay legible.
  textSecondary: "rgba(255,255,255,0.80)",
  scrollbar: "rgba(255,255,255,0.10)",
  scrollbarHover: "rgba(255,255,255,0.18)",
  iconButtonHover: "rgba(255,255,255,0.07)",
  inputBg: "rgba(255,255,255,0.02)",
  chipBg: "rgba(255,255,255,0.06)",
  listItemHover: "rgba(255,255,255,0.04)",
  tableHeadColor: "rgba(255,255,255,0.78)",
  tooltipBg: "#222",
  tooltipText: "#fff",
  dialogShadow: "0 24px 48px rgba(0,0,0,0.55)",
} as const;

const LIGHT = {
  surface: "#ffffff",
  elevated: "#f5f7fa",
  elevated2: "#e9edf3",
  // Borders deep enough that outlined cards / table rows read clearly
  // on pure-white surfaces.
  border: "rgba(15,23,42,0.18)",
  borderStrong: "rgba(15,23,42,0.32)",
  textPrimary: "#0f172a",
  // Secondary / caption text — pushed up to 0.82 so helper text,
  // subtitles and chips stop looking washed out on white.
  textSecondary: "rgba(15,23,42,0.82)",
  scrollbar: "rgba(15,23,42,0.26)",
  scrollbarHover: "rgba(15,23,42,0.40)",
  iconButtonHover: "rgba(15,23,42,0.07)",
  inputBg: "#ffffff",
  chipBg: "rgba(15,23,42,0.10)",
  listItemHover: "rgba(15,23,42,0.06)",
  tableHeadColor: "rgba(15,23,42,0.82)",
  tooltipBg: "#1e293b",
  tooltipText: "#fff",
  dialogShadow: "0 24px 48px rgba(15,23,42,0.18)",
} as const;

function colors(mode: ThemeMode) {
  return mode === "light" ? LIGHT : DARK;
}

// Default accent. The user can override via the in-app color picker.
export const DEFAULT_ACCENT = "#3b82f6";
export const DEFAULT_MODE: ThemeMode = "dark";

// isHexColor reports whether the string looks like a 3- or 6-digit hex CSS
// color, e.g. "#fff" / "#7c83ff". Used to validate user-supplied accent
// values from localStorage / the picker before feeding them to MUI.
export function isHexColor(s: string): boolean {
  return /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(s);
}

// pickContrastingTextColor returns "#0c0c0c" or "#ffffff" depending on which
// has better contrast against the given background. Used so primary buttons
// stay readable regardless of the chosen accent.
export function pickContrastingTextColor(hex: string): string {
  const norm =
    hex.length === 4
      ? "#" +
        hex
          .slice(1)
          .split("")
          .map((c) => c + c)
          .join("")
      : hex;
  const r = parseInt(norm.slice(1, 3), 16);
  const g = parseInt(norm.slice(3, 5), 16);
  const b = parseInt(norm.slice(5, 7), 16);
  // Relative luminance per WCAG, rough approximation.
  const lum = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
  return lum > 0.55 ? "#0c0c0c" : "#ffffff";
}

// ThemePalette exposes only the tokens callers actually read directly.
// MUI's own theme covers the rest (accent goes through `var(--sb-accent)`,
// borderStrong / elevated2 / accentSoft are only consumed inside the
// MUI theme overrides below, never via this struct).
export interface ThemePalette {
  mode: ThemeMode;
  surface: string;
  elevated: string;
  border: string;
}

// buildTheme produces a fresh MUI theme using the given accent color and
// theme mode. All component overrides reference the dynamic accent + mode
// palette so the entire UI updates the moment either changes.
export function buildTheme(
  accentInput: string = DEFAULT_ACCENT,
  modeInput: ThemeMode = DEFAULT_MODE,
): Theme {
  const accent = isHexColor(accentInput) ? accentInput : DEFAULT_ACCENT;
  const mode: ThemeMode = modeInput === "light" ? "light" : "dark";
  const M = colors(mode);
  const accentContrast = pickContrastingTextColor(accent);

  return createTheme({
    palette: {
      mode,
      background: { default: M.surface, paper: M.surface },
      primary: { main: accent, contrastText: accentContrast },
      secondary: { main: mode === "light" ? "#475569" : "#9aa0a6" },
      divider: M.border,
      text: {
        primary: M.textPrimary,
        secondary: M.textSecondary,
      },
      success: { main: "#22c55e" },
      warning: { main: "#f59e0b" },
      error: { main: "#ef4444" },
    },
    shape: { borderRadius: 10 },
    typography: {
      fontFamily:
        'Inter, "SF Pro Display", Roboto, system-ui, -apple-system, "Segoe UI", "Helvetica Neue", Arial, sans-serif',
      h4: { fontWeight: 600, letterSpacing: -0.4 },
      h5: { fontWeight: 600, letterSpacing: -0.3 },
      h6: { fontWeight: 600, letterSpacing: -0.2 },
      button: { fontWeight: 500 },
    },
    components: {
      MuiCssBaseline: {
        styleOverrides: {
          ":root": {
            // Default accent CSS variable. AppThemeProvider overrides this
            // on <html> at runtime; declaring a fallback here means the
            // very first paint never renders with `var(--sb-accent)`
            // pointing at nothing.
            "--sb-accent": DEFAULT_ACCENT,
            // Companion variable holding the readable foreground colour
            // for content sitting on top of `--sb-accent` (e.g. the "+"
            // create IconButton on the CrudPage toolbar). Computed via
            // `pickContrastingTextColor`, kept in lockstep with
            // `--sb-accent` by AppThemeProvider on every accent change.
            "--sb-accent-contrast": accentContrast,
            // Mode-aware surface tones, exposed so component sx blocks can
            // pin sticky / overlapping elements (e.g. CrudPage's pinned
            // Actions column) to exactly the same colour the surrounding
            // Table / TableHead / hovered TableRow renders. Keeping them
            // here means flipping the mode re-emits the trio in lockstep
            // with every other component override.
            "--sb-surface": M.surface,
            "--sb-elevated": M.elevated,
            "--sb-elevated2": M.elevated2,
          },
          html: {
            // Reserve room for the vertical scrollbar even when it isn't
            // present. Without this, anything that briefly hides the
            // scrollbar (autofill, modals, even some browser quirks) shifts
            // the entire viewport horizontally by ~17px and you see a
            // "flick" in the sidebar / topbar text.
            scrollbarGutter: "stable",
          },
          body: {
            backgroundColor: M.surface,
            color: M.textPrimary,
            colorScheme: mode,
          },
          "*::-webkit-scrollbar": { width: 10, height: 10 },
          "*::-webkit-scrollbar-track": { background: "transparent" },
          "*::-webkit-scrollbar-thumb": {
            background: M.scrollbar,
            borderRadius: 8,
            border: "2px solid transparent",
            backgroundClip: "padding-box",
          },
          "*::-webkit-scrollbar-thumb:hover": {
            background: M.scrollbarHover,
            backgroundClip: "padding-box",
          },
          // Theme switching: View Transitions API circular reveal from the
          // toggle's click point. Falls back to instant change in browsers
          // without `document.startViewTransition`.
          //
          // Both pseudo-elements are static images (DOM snapshots), so we
          // override the default cross-fade animation with our own clip-path
          // reveal on the new state. The old snapshot stays at full opacity
          // beneath the new one and is wiped away by the expanding circle.
          // We deliberately do NOT disable transitions on the live DOM
          // during the view-transition — that caused a "bounce" at the end
          // when in-flight ripples / row-hover transitions snapped back.
          "::view-transition-old(root), ::view-transition-new(root)": {
            animation: "none",
            mixBlendMode: "normal",
          },
          "::view-transition-old(root)": {
            zIndex: 1,
          },
          "::view-transition-new(root)": {
            zIndex: 2,
            animation: "theme-reveal 520ms cubic-bezier(0.4, 0, 0.2, 1) forwards",
          },
          "@keyframes theme-reveal": {
            from: {
              clipPath:
                "circle(0px at var(--theme-cx, 50%) var(--theme-cy, 50%))",
            },
            to: {
              clipPath:
                "circle(var(--theme-r, 150vmax) at var(--theme-cx, 50%) var(--theme-cy, 50%))",
            },
          },
        },
      },
      MuiPaper: {
        defaultProps: { elevation: 0 },
        styleOverrides: {
          root: { backgroundImage: "none", backgroundColor: M.surface },
          outlined: {
            borderColor: M.border,
            borderRadius: 12,
          },
        },
      },
      MuiCard: {
        styleOverrides: {
          root: {
            backgroundColor: M.surface,
            backgroundImage: "none",
            border: `1px solid ${M.border}`,
            borderRadius: 12,
            transition:
              "transform 0.18s ease, border-color 0.18s ease, box-shadow 0.18s ease",
          },
        },
      },
      MuiAppBar: {
        styleOverrides: {
          root: {
            backgroundImage: "none",
            backgroundColor: M.elevated,
            boxShadow: "none",
            borderBottom: `1px solid ${M.border}`,
          },
        },
      },
      MuiDrawer: {
        styleOverrides: {
          paper: {
            backgroundImage: "none",
            backgroundColor: M.elevated,
            borderRight: `1px solid ${M.border}`,
          },
        },
      },
      MuiDialog: {
        defaultProps: {
          // Don't lock body scroll — MUI's default behaviour adds
          // `overflow:hidden` + a `padding-right` to <body> equal to the
          // scrollbar width while the modal is open, which shifts every
          // `position:fixed` element (sidebar, topbar) horizontally by
          // ~17px. Keeping the scrollbar avoids that flick.
          disableScrollLock: true,
        },
        styleOverrides: {
          paper: {
            backgroundColor: M.surface,
            backgroundImage: "none",
            border: `1px solid ${M.border}`,
            boxShadow: M.dialogShadow,
          },
        },
      },
      // Same scroll-lock fix for every other Modal-based opener so popping
      // a colour picker, filter dropdown or autocomplete list doesn't yank
      // the layout sideways.
      //
      // We also override the default `Grow` transition with `Fade` for
      // every overlay element. `Grow` scales the element with
      // `scale(0.75, 0.5625) → scale(1, 1)` — an *asymmetric* stretch in
      // which the Y-axis grows from 56% to 100%. With most placements that
      // means the top edge of the popup rises noticeably as the animation
      // ends, which reads as the inner text "snapping upward" at the end
      // of the open animation. A pure opacity fade has no transform at
      // all, so the contents simply appear in their final position.
      MuiPopover: {
        defaultProps: {
          disableScrollLock: true,
          slots: { transition: Fade },
          slotProps: { transition: { timeout: 160 } },
        },
      },
      MuiMenu: {
        defaultProps: {
          disableScrollLock: true,
          slots: { transition: Fade },
          slotProps: { transition: { timeout: 160 } },
        },
      },
      MuiModal: {
        defaultProps: { disableScrollLock: true },
      },
      MuiTableContainer: {
        styleOverrides: { root: { backgroundColor: M.surface } },
      },
      MuiTable: {
        styleOverrides: {
          root: {
            backgroundColor: M.surface,
            width: "100%",
          },
        },
      },
      MuiTableHead: {
        styleOverrides: { root: { backgroundColor: M.elevated } },
      },
      MuiTableRow: {
        styleOverrides: {
          root: {
            transition: "background-color 0.14s ease",
            "&:hover": { backgroundColor: `${M.elevated2} !important` },
            // Tone down the very last row's bottom border so the table edge
            // doesn't read as a hard double line against the Paper outline.
            "&:last-of-type td": { borderBottom: 0 },
          },
        },
      },
      MuiTableCell: {
        styleOverrides: {
          root: {
            color: M.textPrimary,
            borderBottomColor: M.border,
            backgroundColor: "transparent",
            fontSize: 13.5,
            paddingTop: 16,
            paddingBottom: 16,
          },

          sizeSmall: {
            paddingTop: 16,
            paddingBottom: 16,
          },
          head: {
            fontWeight: 600,
            fontSize: 11.5,
            letterSpacing: 0.6,
            textTransform: "uppercase",
            color: M.tableHeadColor,
            backgroundColor: M.elevated,
            borderBottomColor: M.borderStrong,
            // Slimmer header strip — the column-name bar shouldn't take
            // as much vertical space as a data row, just enough to read
            // the label comfortably.
            paddingTop: 8,
            paddingBottom: 8,
          },
        },
      },
      MuiButton: {
        defaultProps: { disableElevation: true },
        styleOverrides: {
          root: {
            textTransform: "none",
            borderRadius: 8,
            paddingInline: 14,
            fontWeight: 500,
            transition:
              "background-color 0.16s ease, border-color 0.16s ease, transform 0.08s ease",
            "&:active": { transform: "translateY(0.5px)" },
          },
          // Accent-coloured rules below reference `var(--sb-accent)` so the
          // emotion class hash stays stable when the user picks a new
          // accent. With a stable class, the declared `transition` actually
          // animates the colour change instead of being voided by a class
          // swap.
          containedPrimary: {
            backgroundColor: "var(--sb-accent)",
            color: accentContrast,
            transition:
              "background-color 0.32s cubic-bezier(0.4,0,0.2,1), color 0.32s cubic-bezier(0.4,0,0.2,1)",
            "&:hover": {
              backgroundColor:
                "color-mix(in srgb, var(--sb-accent) 92%, transparent)",
            },
          },
          outlinedInherit: { borderColor: M.borderStrong },
        },
      },
      MuiIconButton: {
        styleOverrides: {
          root: {
            borderRadius: 8,
            transition: "background-color 0.14s ease, color 0.14s ease",
            // Desktop hover tint — gated on a real hover-capable
            // pointer AND a desktop-width viewport so it never
            // fires on touch devices or in Chrome DevTools' mobile
            // preset (which keeps `(hover: hover)` true because
            // the host machine still has a mouse, but does shrink
            // the viewport, so `(min-width: 600px)` catches it).
            // No companion `:active` / `:focus` rule on the mobile
            // breakpoint by design: per the user's request the
            // button gets zero post-tap visuals on a phone.
            "@media (hover: hover) and (min-width: 600px)": {
              "&:hover": { backgroundColor: M.iconButtonHover },
            },
          },
        },
      },
      // Hide MUI's TouchRipple visual on the mobile / touch
      // breakpoint for every ButtonBase-derived component
      // (IconButton, Button, MenuItem, ListItemButton, …). The
      // ripple is the growing circle that, on phones, can outlive
      // the touch when the touchend event isn't delivered cleanly
      // (drag-off, scroll, sluggish hardware) — that's the halo
      // the user kept seeing after every tap. Desktop pointers
      // fire mouseup / mouseleave reliably, so the ripple animation
      // always completes its exit phase there and we leave it
      // alone. The same `(hover: none), (max-width: 599.95px)` pair
      // is used everywhere else in the theme so the rule fires
      // both on real phones and inside Chrome DevTools'
      // mobile-emulation preset.
      MuiTouchRipple: {
        styleOverrides: {
          root: {
            "@media (hover: none), (max-width: 599.95px)": {
              display: "none",
            },
          },
        },
      },
      MuiOutlinedInput: {
        styleOverrides: {
          root: {
            backgroundColor: M.inputBg,
            "& .MuiOutlinedInput-notchedOutline": {
              borderColor: M.border,
              transition: "border-color 0.32s cubic-bezier(0.4,0,0.2,1)",
            },
            "&:hover .MuiOutlinedInput-notchedOutline": { borderColor: M.borderStrong },
            "&.Mui-focused .MuiOutlinedInput-notchedOutline": {
              borderColor: "var(--sb-accent)",
              borderWidth: 1,
            },
            // Suppress the browser's autofill highlight (Firefox paints
            // the field with a translucent blue band, Chrome/Safari with
            // yellow). The cell's own background already reads as
            // "active", so the autofill colour is purely noise.
            //
            // WebKit (Chrome/Safari) doesn't honour normal
            // `background-color` overrides on autofilled inputs — the
            // canonical workaround is a giant inset `box-shadow` that
            // visually replaces the background, plus `text-fill-color`
            // for the foreground colour the engine forces. Firefox uses
            // the standard `:autofill` pseudo and respects ordinary
            // `background-color` / `filter` overrides since v86.
            "& input:-webkit-autofill, & input:-webkit-autofill:hover, & input:-webkit-autofill:focus, & input:-webkit-autofill:active":
              {
                WebkitBoxShadow: `0 0 0 1000px ${M.surface} inset !important`,
                WebkitTextFillColor: `${M.textPrimary} !important`,
                caretColor: `${M.textPrimary} !important`,
                // The 5000s transition delay keeps the engine from
                // re-running the autofill style transition on every
                // focus / blur, which would re-paint the highlight.
                transition: "background-color 5000s ease-in-out 0s",
              },
            "& input:autofill": {
              backgroundColor: "transparent !important",
              filter: "none !important",
              color: `${M.textPrimary} !important`,
            },
          },
        },
      },
      MuiChip: {
        styleOverrides: {
          root: {
            backgroundColor: M.chipBg,
            color: M.textPrimary,
            fontWeight: 500,
            borderRadius: 6,
          },
          // "Backlit" badge with accent glow — used for selected items in
          // multi-select boxes so they stand out without being garish.
          colorPrimary: {
            backgroundColor:
              "color-mix(in srgb, var(--sb-accent) 14%, transparent)",
            color: "var(--sb-accent)",
            border:
              "1px solid color-mix(in srgb, var(--sb-accent) 35%, transparent)",
            transition:
              "background-color 0.32s cubic-bezier(0.4,0,0.2,1), color 0.32s cubic-bezier(0.4,0,0.2,1), border-color 0.32s cubic-bezier(0.4,0,0.2,1)",
          },
          colorSuccess: { backgroundColor: alpha("#22c55e", 0.18), color: mode === "light" ? "#15803d" : "#86efac" },
          colorWarning: { backgroundColor: alpha("#f59e0b", 0.18), color: mode === "light" ? "#a16207" : "#fcd34d" },
          colorError: { backgroundColor: alpha("#ef4444", 0.18), color: mode === "light" ? "#b91c1c" : "#fca5a5" },
        },
      },
      MuiListItemButton: {
        styleOverrides: {
          root: {
            borderRadius: 8,
            marginInline: 8,
            paddingBlock: 6,
            transition: "background-color 0.32s cubic-bezier(0.4,0,0.2,1)",
            "&.Mui-selected": {
              // Mode-aware accent tint: 16% on dark, 12% on light so the
              // selected pill reads cleanly on both surfaces.
              backgroundColor: `color-mix(in srgb, var(--sb-accent) ${
                mode === "light" ? 12 : 16
              }%, transparent)`,
              "&:hover": {
                backgroundColor:
                  "color-mix(in srgb, var(--sb-accent) 22%, transparent)",
              },
              "& .MuiListItemIcon-root": {
                color: "var(--sb-accent)",
                transition: "color 0.32s cubic-bezier(0.4,0,0.2,1)",
              },
              "& .MuiListItemText-primary": {
                color: mode === "light" ? "#0f172a" : "#fff",
                // Visually heavier on the active item without bumping
                // `fontWeight`. Different font weights have different glyph
                // advance widths, so toggling between 400 and 600 caused a
                // ~1px horizontal jump every time the selection changed (or
                // re-rendered on hover); painting an extra "stroke" via
                // text-shadow gives the same heaviness with zero layout
                // impact.
                textShadow: "0 0 0.6px currentColor",
              },
            },
            "&:hover": { backgroundColor: M.listItemHover },
          },
        },
      },
      MuiListItemIcon: {
        styleOverrides: {
          root: { minWidth: 36, color: M.textSecondary },
        },
      },
      MuiTooltip: {
        defaultProps: {
          // See the MuiPopover comment above — Grow's asymmetric scale
          // makes tooltip text appear to "snap upward" at the end of the
          // open animation. A pure opacity fade has no transform so the
          // contents simply appear in their final position.
          slots: { transition: Fade },
          slotProps: { transition: { timeout: 140 } },
        },
        styleOverrides: {
          tooltip: {
            backgroundColor: M.tooltipBg,
            color: M.tooltipText,
            fontSize: 12,
            border: `1px solid ${M.border}`,
          },
        },
      },
      MuiAlert: {
        styleOverrides: {
          root: { borderRadius: 10 },
        },
      },
    },
  });
}

export function buildPalette(
  modeInput: ThemeMode = DEFAULT_MODE,
): ThemePalette {
  const mode: ThemeMode = modeInput === "light" ? "light" : "dark";
  const M = colors(mode);
  return {
    mode,
    surface: M.surface,
    elevated: M.elevated,
    border: M.border,
  };
}
