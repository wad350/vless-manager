import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { flushSync } from "react-dom";
import { CssBaseline, ThemeProvider } from "@mui/material";
import {
  DEFAULT_ACCENT,
  DEFAULT_MODE,
  buildPalette,
  buildTheme,
  isHexColor,
  pickContrastingTextColor,
  type ThemeMode,
  type ThemePalette,
} from "../theme";

const ACCENT_KEY = "sing-box-admin:accent";
const MODE_KEY = "sing-box-admin:mode";

type ThemeOrigin = { clientX: number; clientY: number } | ReactMouseEvent | MouseEvent;

interface AccentContextValue {
  accent: string;
  setAccent: (color: string) => void;
  resetAccent: () => void;
  mode: ThemeMode;
  setMode: (mode: ThemeMode, origin?: ThemeOrigin) => void;
  toggleMode: (origin?: ThemeOrigin) => void;
  palette: ThemePalette;
}

const AccentContext = createContext<AccentContextValue | undefined>(undefined);

function loadStoredAccent(): string {
  try {
    const raw = localStorage.getItem(ACCENT_KEY);
    if (raw && isHexColor(raw)) return raw;
  } catch {
    /* ignore */
  }
  return DEFAULT_ACCENT;
}

function loadStoredMode(): ThemeMode {
  try {
    const raw = localStorage.getItem(MODE_KEY);
    if (raw === "light" || raw === "dark") return raw;
  } catch {
    /* ignore */
  }
  return DEFAULT_MODE;
}

// AppThemeProvider owns the accent color + theme mode, persists both
// across reloads, and rebuilds the MUI theme on every change so the
// whole UI updates instantly.
export function AppThemeProvider({ children }: { children: ReactNode }) {
  const [accent, setAccentState] = useState<string>(() => loadStoredAccent());
  const [mode, setModeState] = useState<ThemeMode>(() => loadStoredMode());

  // Wrap a state update in a circular-reveal animation that radiates from
  // the click point (when supported) using the View Transitions API.
  // Fallbacks: prefers-reduced-motion → no animation; older browsers without
  // `document.startViewTransition` also fall back to instant state change.
  const runWithTransition = useCallback((apply: () => void, origin?: ThemeOrigin) => {
    const root = document.documentElement;
    const reduced = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    type ViewTransitionDoc = Document & {
      startViewTransition?: (cb: () => void) => unknown;
    };
    const docVT = document as ViewTransitionDoc;
    if (reduced || typeof docVT.startViewTransition !== "function") {
      apply();
      return;
    }
    // Default to the screen centre when no event is supplied (e.g. keyboard
    // toggling). Compute the maximum radius so the circle always reaches the
    // furthest corner regardless of click position.
    const w = window.innerWidth;
    const h = window.innerHeight;
    const cx = origin?.clientX ?? w / 2;
    const cy = origin?.clientY ?? h / 2;
    const r = Math.hypot(Math.max(cx, w - cx), Math.max(cy, h - cy));
    root.style.setProperty("--theme-cx", `${cx}px`);
    root.style.setProperty("--theme-cy", `${cy}px`);
    root.style.setProperty("--theme-r", `${r}px`);
    docVT.startViewTransition(() => {
      // flushSync forces React to commit the new theme synchronously inside
      // the snapshot callback; otherwise the API would capture the old DOM.
      flushSync(apply);
    });
  }, []);

  const persistMode = useCallback((next: ThemeMode) => {
    try {
      localStorage.setItem(MODE_KEY, next);
    } catch {
      /* ignore */
    }
  }, []);

  const setMode = useCallback(
    (next: ThemeMode, origin?: ThemeOrigin) => {
      if (next !== "light" && next !== "dark") return;
      runWithTransition(() => {
        setModeState(next);
        persistMode(next);
      }, origin);
    },
    [runWithTransition, persistMode],
  );

  const toggleMode = useCallback(
    (origin?: ThemeOrigin) => {
      runWithTransition(() => {
        setModeState((prev) => {
          const next: ThemeMode = prev === "light" ? "dark" : "light";
          persistMode(next);
          return next;
        });
      }, origin);
    },
    [runWithTransition, persistMode],
  );

  const setAccent = useCallback((color: string) => {
    if (!isHexColor(color)) return;
    setAccentState(color);
    try {
      localStorage.setItem(ACCENT_KEY, color);
    } catch {
      /* ignore */
    }
  }, []);

  const resetAccent = useCallback(() => {
    setAccentState(DEFAULT_ACCENT);
    try {
      localStorage.removeItem(ACCENT_KEY);
    } catch {
      /* ignore */
    }
  }, []);

  // Accent colour is exposed as a CSS variable on <html>. Specific MUI
  // components in the theme reference `var(--sb-accent)` instead of the
  // raw hex, which keeps their emotion class hash stable when the accent
  // changes — so a CSS `transition` declared on those rules animates
  // smoothly from the old colour to the new one.
  //
  // We use `useLayoutEffect` instead of `useEffect` so the variable is
  // applied before the browser paints; otherwise the very first frame
  // would render with no `--sb-accent` set and accent elements would
  // briefly flash unstyled before the effect ran.
  useLayoutEffect(() => {
    const root = document.documentElement;
    root.style.setProperty("--sb-accent", accent);
    // Companion contrast colour (black or white, picked by relative
    // luminance) for foreground content layered on top of the accent —
    // the "+" create IconButton on the CrudPage toolbar reads this var
    // to keep its glyph readable on accents of any luminance. Without
    // setting it here the IconButton's `var(--sb-accent-contrast,
    // #ffffff)` always fell back to white, so a light/yellow accent
    // produced an almost-invisible glyph that "didn't change with the
    // theme".
    root.style.setProperty("--sb-accent-contrast", pickContrastingTextColor(accent));
  }, [accent]);

  // The inline FOUC script in `index.html` stamps an initial
  // `background-color` and `color-scheme` onto the <html> element so
  // the page doesn't paint white before the bundle loads. Inline
  // styles win over MUI's CssBaseline, so we have to keep those
  // properties in sync here whenever the mode flips — otherwise
  // `<html>` keeps the page-load colour and the document looks
  // half-themed (background stuck on the old tone, text/buttons
  // updated, etc.).
  //
  // The same FOUC script also sets inline `background-color` + `color`
  // on <body> (so the very first paint after reload is fully themed),
  // and inline styles win over the emotion class CssBaseline injects.
  // If we don't refresh those here too, `body.color` stays at the
  // page-load value forever, which makes every `color="inherit"`
  // component (Layout's "Sign out", CrudPage's "Filters" / "Clear",
  // the colour-picker button) read with stale text colour after a
  // theme toggle — the labels then "snap back" to the correct colour
  // only on full reload, which is exactly what was reported.
  useLayoutEffect(() => {
    const root = document.documentElement;
    const bg = mode === "light" ? "#ffffff" : "#141414";
    const fg = mode === "light" ? "#0f172a" : "#f5f5f5";
    root.style.backgroundColor = bg;
    root.style.colorScheme = mode;
    if (document.body) {
      document.body.style.backgroundColor = bg;
      document.body.style.color = fg;
    }
    // <meta name="theme-color"> drives the mobile browser chrome /
    // installed-PWA window colour. We update it from the same effect
    // that sets the document background so the two values can never
    // disagree (a previous arrangement had a separate `useEffect` on
    // `palette` re-writing the meta a frame later, which produced a
    // brief mismatched flash on mode toggle).
    const meta = document.querySelector<HTMLMetaElement>(
      'meta[name="theme-color"]',
    );
    if (meta) meta.content = bg;
  }, [mode]);

  // Sync between tabs.
  useEffect(() => {
    const handler = (e: StorageEvent) => {
      if (e.key === ACCENT_KEY) setAccentState(loadStoredAccent());
      if (e.key === MODE_KEY) setModeState(loadStoredMode());
    };
    window.addEventListener("storage", handler);
    return () => window.removeEventListener("storage", handler);
  }, []);

  const theme = useMemo(() => buildTheme(accent, mode), [accent, mode]);
  // The exposed palette only carries mode-derived tokens (surface /
  // elevated / border), so it doesn't need to invalidate on accent
  // changes — accent flows through `var(--sb-accent)` instead.
  const palette = useMemo(() => buildPalette(mode), [mode]);

  const value = useMemo<AccentContextValue>(
    () => ({ accent, setAccent, resetAccent, mode, setMode, toggleMode, palette }),
    [accent, setAccent, resetAccent, mode, setMode, toggleMode, palette],
  );

  return (
    <AccentContext.Provider value={value}>
      <ThemeProvider theme={theme}>
        <CssBaseline />
        {children}
      </ThemeProvider>
    </AccentContext.Provider>
  );
}

export function useAccent(): AccentContextValue {
  const ctx = useContext(AccentContext);
  if (!ctx) throw new Error("useAccent must be used within AppThemeProvider");
  return ctx;
}
