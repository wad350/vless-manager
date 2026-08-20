import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { LocalizationProvider } from "@mui/x-date-pickers";
import { AdapterDayjs } from "@mui/x-date-pickers/AdapterDayjs";
// Bundle Inter locally — Vite hashes the woff/woff2 files into the build
// so the SPA serves them from the same origin and never reaches out to
// fonts.gstatic.com. Each weight import is the only side-effect path that
// registers an @font-face rule for that weight.
import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
import { App } from "./App";
import { AuthProvider } from "./auth/AuthContext";
import { AppThemeProvider } from "./theme/AppThemeProvider";
import { NotificationsProvider } from "./notifications/NotificationsProvider";

const container = document.getElementById("root");
if (!container) throw new Error("missing #root");

createRoot(container).render(
  <StrictMode>
    <AppThemeProvider>
      {/* NotificationsProvider sits between the theme and the rest of
          the app so toast Alerts pick up the active palette, and so
          AuthProvider can call `useNotify` from its global 401 handler
          (clearing credentials + announcing "Session expired" in one
          shot). */}
      <NotificationsProvider>
        {/* LocalizationProvider feeds the MUI X date/time pickers used by the
            datetime-range filter. dayjs is small (~7 KB gzipped) and matches
            the format strings the manager API accepts. */}
        <LocalizationProvider dateAdapter={AdapterDayjs}>
          {/* BrowserRouter so deep routes (e.g. /users) are real URLs
              shareable across users / bookmarkable / discoverable by
              copy-paste. The Go backend serves the SPA's index.html for
              every unknown path (see service/admin_panel/service.go), so
              the browser always boots the bundle and react-router takes
              over client-side. */}
          <BrowserRouter>
            <AuthProvider>
              <App />
            </AuthProvider>
          </BrowserRouter>
        </LocalizationProvider>
      </NotificationsProvider>
    </AppThemeProvider>
  </StrictMode>,
);
