import {
  Box,
  Button,
  IconButton,
  InputAdornment,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import VisibilityIcon from "@mui/icons-material/Visibility";
import VisibilityOffIcon from "@mui/icons-material/VisibilityOff";
import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import {
  ApiError,
  UnauthorizedError,
  clearLoginDraft,
  loadLoginDraft,
  ping,
  saveLoginDraft,
} from "../api/client";
import { useNotify } from "../notifications/NotificationsProvider";
import { LoginBackdropMesh } from "../components/LoginBackdropMesh";
import { LoginThemeControls } from "../components/LoginThemeControls";
import brandIcon from "../assets/icon.svg";

export function LoginPage() {
  const { login } = useAuth();
  const notify = useNotify();
  // Pre-fill from the persisted login draft so the user does not have to
  // retype their URL + key after closing the tab or logging out.
  const [baseUrl, setBaseUrl] = useState(() => loadLoginDraft().baseUrl);
  const [apiKey, setApiKey] = useState(() => loadLoginDraft().apiKey);
  const [busy, setBusy] = useState(false);
  // Toggles whether the API key is rendered as plain text or masked
  // dots. Defaults to masked so the page never paints a credential in
  // clear text on first load — the user has to opt in to peek.
  const [showApiKey, setShowApiKey] = useState(false);

  // Persist the form on every keystroke. `saveLoginDraft` itself swallows
  // any storage errors (private mode / quota), so the effect is a no-op
  // there rather than throwing into the React tree. We don't bother with
  // a `beforeunload` flush — the per-keystroke write already keeps the
  // saved copy in sync with the latest field value at all times.
  useEffect(() => {
    saveLoginDraft({ baseUrl, apiKey });
  }, [baseUrl, apiKey]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const trimmedUrl = baseUrl.trim();
      const trimmedKey = apiKey.trim();
      if (!trimmedUrl || !trimmedKey) throw new Error("API URL and key are required");
      await ping({ baseUrl: trimmedUrl, apiKey: trimmedKey });
      // Auth itself is the source of truth once signed in — drop the draft.
      clearLoginDraft();
      login({ baseUrl: trimmedUrl, apiKey: trimmedKey });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // Errors are surfaced exclusively through the global toast stack so
      // the form's height stays constant on a rejected attempt. An inline
      // <Alert> inside the Paper would grow the form and, combined with
      // the parent's vertical centering, make the whole card "bounce" up
      // and down each time the user mistypes the API key. The toast still
      // carries a contextual message ("Authorization failed" /
      // "Connection error" / generic API failure) so the user sees a
      // clear cause for the rejected sign-in.
      // 401s are a normal path here (typo'd API key); we explicitly
      // map them to the auth-error wording rather than the generic
      // ApiError formatter from notifyApiError so the message reads
      // naturally on the login screen.
      if (err instanceof UnauthorizedError) {
        notify.error("Authorization failed — check the API key.");
      } else if (err instanceof ApiError && err.status === 0) {
        notify.error(
          `Connection error — could not reach the manager API: ${err.body || err.message}`,
        );
      } else if (err instanceof ApiError) {
        notify.error(`Sign-in failed (HTTP ${err.status}): ${err.body || err.message}`);
      } else {
        notify.error(`Sign-in failed: ${message}`);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box
      sx={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        bgcolor: "background.default",
        position: "relative",
        overflow: "hidden",
        p: 2,
      }}
    >
      <LoginBackdropMesh />
      <LoginThemeControls />
      <Paper
        component="form"
        onSubmit={submit}
        variant="outlined"
        sx={{
          p: { xs: 3, sm: 4.5 },
          width: "100%",
          maxWidth: 440,
          position: "relative",
          // Bump the form above the absolutely-positioned backdrop so
          // the animated network stays strictly behind the card. Without
          // this the Paper (a flex item in normal flow) would paint
          // before positioned siblings and the SVG would sit on top.
          zIndex: 1,
          // Drop shadow removed: the animated backdrop already
          // gives the page enough depth, and the form's outline
          // border is enough to detach it from the moving graphics
          // behind it without needing a shadow on top.
          boxShadow: "none",
        }}
      >
        <Stack spacing={3}>
          {/* Brand — icon + two-line wordmark grouped inside a single
              flex row, mirroring the sidebar header in Layout.tsx.
              Sizes are scaled up: the sidebar uses 32 px icon /
              17 px wordmark / 10 px subtitle; here those are
              multiplied by 44/32 to keep proportions identical
              while filling the larger card surface. The text
              wrapper carries a small paddingBottom so
              `alignItems: center` optically centres the two-line
              text against the icon without touching any margins
              on the individual spans. */}
          <Box
            sx={{
              display: "flex",
              alignItems: "center",
              gap: 2,
            }}
          >
            <Box
              component="img"
              src={brandIcon}
              alt="sing-box"
              sx={{
                width: 44,
                height: 44,
                flexShrink: 0,
                display: "block",
                objectFit: "contain",
              }}
            />
            <Box
              sx={{
                minWidth: 0,
                paddingBottom: "6px",
                whiteSpace: "nowrap",
              }}
            >
              <Box
                component="span"
                sx={(t) => ({
                  display: "block",
                  margin: 0,
                  height: 30,
                  fontFamily: t.typography.fontFamily,
                  fontWeight: 700,
                  fontSize: 24,
                  letterSpacing: -0.6,
                  lineHeight: "30px",
                  color: t.palette.text.primary,
                })}
              >
                Sing-box
              </Box>
              <Box
                component="span"
                sx={{
                  display: "block",
                  margin: 0,
                  fontSize: 12,
                  fontWeight: 700,
                  letterSpacing: 2.4,
                  lineHeight: "15px",
                  marginTop: "3px",
                  textTransform: "uppercase",
                  color: "var(--sb-accent)",
                  transition: "color 0.32s cubic-bezier(0.4,0,0.2,1)",
                }}
              >
                extended
              </Box>
            </Box>
          </Box>
          <Stack spacing={2}>
            <TextField
              fullWidth
              size="small"
              label="API base URL"
              placeholder="http://127.0.0.1:8090"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              required
              autoFocus
            />
            <TextField
              fullWidth
              size="small"
              // `type` flips between password and text driven by the
              // visibility toggle below. We keep `autoComplete="off"`
              // so a browser password manager doesn't autofill the
              // wrong credential into the API key slot.
              type={showApiKey ? "text" : "password"}
              label="API key"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              required
              autoComplete="off"
              slotProps={{
                input: {
                  endAdornment: (
                    <InputAdornment position="end">
                      <IconButton
                        // `onMouseDown` prevent-default keeps the input
                        // focused when the user clicks the toggle —
                        // without it the field would lose focus and any
                        // autofocus logic would have to chase the user.
                        onMouseDown={(e) => e.preventDefault()}
                        onClick={() => setShowApiKey((v) => !v)}
                        edge="end"
                        size="small"
                        // Screen-reader label kept (it's not a visible
                        // description), but no Tooltip wrapper — per
                        // request the eye icon stays "quiet" with no
                        // hover hint.
                        aria-label={
                          showApiKey ? "Hide API key" : "Show API key"
                        }
                      >
                        {showApiKey ? (
                          <VisibilityOffIcon fontSize="small" />
                        ) : (
                          <VisibilityIcon fontSize="small" />
                        )}
                      </IconButton>
                    </InputAdornment>
                  ),
                },
              }}
            />
          </Stack>
          <Button
            type="submit"
            variant="contained"
            color="primary"
            size="large"
            disabled={busy}
            sx={{ py: 1.25 }}
          >
            {busy ? "Connecting…" : "Sign in"}
          </Button>
          <Typography variant="caption" color="text.secondary" textAlign="center">
            Credentials are stored locally in your browser only.
          </Typography>
        </Stack>
      </Paper>
    </Box>
  );
}
