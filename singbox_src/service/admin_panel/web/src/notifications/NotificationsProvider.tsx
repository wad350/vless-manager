import { Alert, Box, Collapse, IconButton, Slide } from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ApiError, UnauthorizedError } from "../api/client";

// NotificationsProvider drives the small stack of "toast"-style alerts
// that flash in from the top-right whenever the panel completes (or
// fails) a user-visible action: a record was created / deleted, a CRUD
// request hit a network error, the manager-api returned 401, etc. The
// alerts auto-dismiss after a short delay and de-duplicate so a flurry
// of identical errors (e.g. a paginated reload that retries) shows up as
// a single chip.
//
// Two consumption styles are exposed via `useNotify`:
//   - `notify({ severity, message })` for the full payload
//   - convenience shorthands `notify.success(msg) / .error(msg) / …`
//
// `notifyApiError` is a pre-canned error-toast helper used by every
// CRUD callsite — it picks a useful description based on the error
// type (connection vs. unauthorized vs. generic API) so callers don't
// have to hand-format messages.

export type NotificationSeverity = "success" | "info" | "warning" | "error";

export interface NotificationOptions {
  severity: NotificationSeverity;
  message: string;
  // Auto-dismiss timer (ms). `null` keeps the toast on screen until the
  // user dismisses it manually. Defaults to ~4.5 s, long enough to read
  // a sentence-length message without parking on screen.
  duration?: number | null;
}

interface QueuedNotification extends NotificationOptions {
  id: number;
  // Drives the enter/exit transitions: items start with `open: true`,
  // flip to `false` when the user clicks × or the auto-timer fires, and
  // are finally removed from the array when the Collapse exit transition
  // completes (via its `onExited` callback). Keeping the entry alive in
  // state for the duration of the exit transition is what gives the
  // close animation room to play instead of snapping the chip out.
  open: boolean;
}

export interface NotificationsApi {
  notify: (n: NotificationOptions) => number;
  success: (message: string, options?: Partial<NotificationOptions>) => number;
  info: (message: string, options?: Partial<NotificationOptions>) => number;
  warning: (message: string, options?: Partial<NotificationOptions>) => number;
  error: (message: string, options?: Partial<NotificationOptions>) => number;
  dismiss: (id: number) => void;
}

const Ctx = createContext<NotificationsApi | undefined>(undefined);

const DEFAULT_DURATION = 4500;
// Cap on the number of toasts shown at once. Anything over this limit
// drops the oldest entry — a misbehaving callsite that fires hundreds
// of notifications in a row should not be able to fill the viewport.
const MAX_VISIBLE = 5;
// Transition timings (ms). Both Slide (horizontal motion) and Collapse
// (height collapse) share the same timeouts so the chip's slide-out and
// the stack's reflow finish together — a mismatch here would leave a
// phantom gap or a half-collapsed shadow visible for a frame after the
// chip itself has gone.
//
// 250 ms matches MUI's "standard" duration: the toast slides in /
// out at a familiar pace, neither lingering on screen nor flashing
// past too fast to register.
const TRANSITION_MS = 250;
const TRANSITION_TIMEOUT = { enter: TRANSITION_MS, exit: TRANSITION_MS } as const;
// Easing is intentionally left to MUI's defaults — Slide and Collapse
// fall back to `theme.transitions.easing.easeOut` on enter and
// `easeIn` on exit, which gives a clean "fade out to the right" feel
// without any overshoot or bounce.

export function NotificationsProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<QueuedNotification[]>([]);
  const idRef = useRef(0);
  // Track the active auto-dismiss timers so unmounting the provider (or a
  // very stale toast) cleans them up rather than firing into a torn-down
  // tree.
  const timers = useRef<Map<number, number>>(new Map());

  // dismiss flips the toast's `open` flag to false, kicking off its exit
  // transition (Slide back out to the right + Collapse height to 0). The
  // entry stays in `items` until `finalize` removes it once the Collapse
  // `onExited` callback fires — that is what gives the close animation
  // time to play instead of snapping the chip out of the DOM. Calling
  // `dismiss` more than once for the same id (auto-timer + user click
  // race) is a safe no-op.
  const dismiss = useCallback((id: number) => {
    setItems((prev) =>
      prev.map((n) => (n.id === id && n.open ? { ...n, open: false } : n)),
    );
    const handle = timers.current.get(id);
    if (handle !== undefined) {
      window.clearTimeout(handle);
      timers.current.delete(id);
    }
  }, []);

  // finalize is invoked by the Collapse `onExited` callback once the exit
  // transition has fully played out, removing the entry from state and
  // letting React unmount the underlying DOM nodes.
  const finalize = useCallback((id: number) => {
    setItems((prev) => prev.filter((n) => n.id !== id));
  }, []);

  const notify = useCallback(
    (n: NotificationOptions): number => {
      const id = ++idRef.current;
      setItems((prev) => {
        // De-duplicate: if the most recent *visible* toast carries the
        // same severity and message, swallow this one. A failed reload
        // that retries shouldn't stack identical chips on top of each
        // other. Items already in their exit transition (`open: false`)
        // are skipped so a quickly-cleared duplicate doesn't suppress a
        // legitimate repeat.
        for (let i = prev.length - 1; i >= 0; i--) {
          const p = prev[i];
          if (!p.open) continue;
          if (p.severity === n.severity && p.message === n.message) {
            return prev;
          }
          break;
        }
        const next: QueuedNotification[] = [...prev, { ...n, id, open: true }];
        // Cap the number of *visible* (still-open) toasts. Any overflow
        // gets gracefully dismissed — flipped to `open: false` — so the
        // overflowing oldest entry plays its close animation instead of
        // popping out of existence. `finalize` will remove it from the
        // array when its transition finishes.
        let visible = 0;
        for (const p of next) if (p.open) visible++;
        let toClose = visible - MAX_VISIBLE;
        for (let i = 0; i < next.length && toClose > 0; i++) {
          if (next[i].open) {
            next[i] = { ...next[i], open: false };
            const stale = timers.current.get(next[i].id);
            if (stale !== undefined) {
              window.clearTimeout(stale);
              timers.current.delete(next[i].id);
            }
            toClose--;
          }
        }
        return next;
      });
      const duration = n.duration === undefined ? DEFAULT_DURATION : n.duration;
      if (duration !== null && duration > 0) {
        const handle = window.setTimeout(() => dismiss(id), duration);
        timers.current.set(id, handle);
      }
      return id;
    },
    [dismiss],
  );

  // Cleanup pending timers on unmount.
  useEffect(() => {
    const map = timers.current;
    return () => {
      for (const handle of map.values()) window.clearTimeout(handle);
      map.clear();
    };
  }, []);

  const value = useMemo<NotificationsApi>(
    () => ({
      notify,
      success: (message, options) => notify({ severity: "success", message, ...options }),
      info: (message, options) => notify({ severity: "info", message, ...options }),
      warning: (message, options) => notify({ severity: "warning", message, ...options }),
      error: (message, options) => notify({ severity: "error", message, ...options }),
      dismiss,
    }),
    [notify, dismiss],
  );

  return (
    <Ctx.Provider value={value}>
      {children}
      {/* Stack of toasts pinned to the top-right of the viewport. A
          high `zIndex` keeps them above MUI Dialog (1300) and Drawer
          (1200) so a notification triggered from inside a modal
          (e.g. CRUD form submission error) is still visible without
          having to close the dialog first.

          `pointerEvents: "none"` on the container lets clicks pass
          through to the page underneath the empty space between
          alerts; each Alert re-enables pointer events for itself so
          its own close button still works. */}
      <Box
        sx={{
          position: "fixed",
          top: { xs: 4, sm: 8 },
          right: { xs: 12, sm: 16 },
          zIndex: 2000,
          display: "flex",
          flexDirection: "column",
          // No `gap` here on purpose — the inter-toast spacing lives
          // inside each Collapse below (as a top padding). That way
          // the spacing height animates *together* with the toast
          // instead of leaving a stranded gap during the close
          // animation, and the stack reflows smoothly when an entry
          // disappears.
          maxWidth: "min(92vw, 420px)",
          pointerEvents: "none",
        }}
        aria-live="polite"
        aria-atomic="false"
      >
        {items.map((n) => (
          // Collapse owns the *vertical* exit motion: when `open` flips
          // to false it shrinks the chip's height to 0, pulling the
          // toasts below it up smoothly. Its `onExited` callback then
          // hands off to `finalize`, which removes the entry from
          // state once the animation has fully played out.
          <Collapse
            key={n.id}
            in={n.open}
            appear
            timeout={TRANSITION_TIMEOUT}
            onExited={() => finalize(n.id)}
          >
            {/* Padding-top on the inner wrapper — not a parent flex gap
                — so the spacing collapses together with the toast on
                exit. The first toast inherits the same 8 px top
                padding; combined with the container's `top: 8` it
                lines up at 16 px from the viewport edge, which reads
                cleanly without an explicit "no padding for index 0"
                special case (which would jump-snap when the topmost
                toast is dismissed). */}
            <Box sx={{ pt: 1 }}>
              {/* Slide owns the *horizontal* motion: enters by sliding
                  left from off-screen on the right, exits by sliding
                  back out to the right. Sharing the timeout shape with
                  Collapse keeps the two transitions in lockstep so the
                  chip lands / leaves cleanly. */}
              <Slide
                in={n.open}
                direction="left"
                appear
                timeout={TRANSITION_TIMEOUT}
              >
                <Alert
                  severity={n.severity}
                  variant="filled"
                  onClose={() => dismiss(n.id)}
                  action={
                    <IconButton
                      aria-label="Dismiss notification"
                      size="small"
                      color="inherit"
                      onClick={() => dismiss(n.id)}
                    >
                      <CloseIcon fontSize="inherit" />
                    </IconButton>
                  }
                  sx={(theme) => {
                    // Force the toast contents to follow the active
                    // theme rather than MUI's default "always white on
                    // a coloured background" rule for the filled Alert
                    // variant: white text in dark mode, black text in
                    // light mode. Applied to the message body, the
                    // severity icon, and the dismiss button so all
                    // three follow the same contrast rule and read as
                    // a single tonal pair.
                    const fg = theme.palette.mode === "dark" ? "#ffffff" : "#000000";
                    return {
                      pointerEvents: "auto",
                      boxShadow: 6,
                      alignItems: "center",
                      color: fg,
                      "& .MuiAlert-icon": { color: fg },
                      "& .MuiAlert-action": { color: fg },
                      "& .MuiAlert-action .MuiIconButton-root": { color: fg },
                      // Long error bodies (e.g. an HTTP response excerpt)
                      // should wrap rather than overflow the chip and steal
                      // the close button.
                      "& .MuiAlert-message": {
                        color: fg,
                        wordBreak: "break-word",
                        overflowWrap: "anywhere",
                      },
                    };
                  }}
                >
                  {n.message}
                </Alert>
              </Slide>
            </Box>
          </Collapse>
        ))}
      </Box>
    </Ctx.Provider>
  );
}

export function useNotify(): NotificationsApi {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useNotify must be used within NotificationsProvider");
  return ctx;
}

// notifyApiError emits a "toast"-style error notification for an exception
// thrown by the API client, picking a useful description based on the
// error type:
//
//   - `UnauthorizedError` (HTTP 401) — silently skipped: the global
//     handler in AuthContext already surfaces a "session expired"
//     toast and redirects to the login screen, so emitting another
//     here would double up.
//   - `ApiError` with `status === 0` — connection/network error
//     (fetch itself failed before a response arrived). Surfaced as a
//     "Connection error" toast so the user understands the panel
//     didn't reach the manager-api at all.
//   - any other `ApiError` — formatted with the HTTP status and
//     server-provided body excerpt.
//   - anything else — message of the underlying Error / String fallback.
export function notifyApiError(
  notify: NotificationsApi,
  prefix: string,
  e: unknown,
): void {
  if (e instanceof UnauthorizedError) return;
  if (e instanceof ApiError && e.status === 0) {
    notify.error(`${prefix}: connection error — ${e.body || e.message}`);
    return;
  }
  if (e instanceof ApiError) {
    notify.error(`${prefix} (HTTP ${e.status}): ${e.body || e.message}`);
    return;
  }
  notify.error(`${prefix}: ${e instanceof Error ? e.message : String(e)}`);
}
