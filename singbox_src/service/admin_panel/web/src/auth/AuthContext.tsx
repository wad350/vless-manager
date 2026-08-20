import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  type Api,
  type AuthInfo,
  clearAuth,
  loadAuth,
  makeApi,
  saveAuth,
  saveLoginDraft,
  setUnauthorizedHandler,
} from "../api/client";
import { useNotify } from "../notifications/NotificationsProvider";

interface AuthContextValue {
  auth: AuthInfo | null;
  api: Api | null;
  login: (auth: AuthInfo) => void;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [auth, setAuth] = useState<AuthInfo | null>(() => loadAuth());
  const notify = useNotify();

  // keep tabs in sync
  useEffect(() => {
    const handler = (e: StorageEvent) => {
      if (e.key === null || e.key === "sing-box-admin:auth") setAuth(loadAuth());
    };
    window.addEventListener("storage", handler);
    return () => window.removeEventListener("storage", handler);
  }, []);

  const login = useCallback((next: AuthInfo) => {
    saveAuth(next);
    setAuth(next);
  }, []);

  const logout = useCallback(() => {
    // Remember the URL the user was just connected to so the login form is
    // pre-filled on the way back. The key is *not* preserved.
    setAuth((prev) => {
      if (prev?.baseUrl) {
        saveLoginDraft({ baseUrl: prev.baseUrl, apiKey: "" });
      }
      return null;
    });
    clearAuth();
  }, []);

  // Globally trap 401 → kick back to the login screen.
  //
  // Two flavours, distinguished by whether the user *was* signed in
  // when the 401 hit:
  //   - prev !== null → an active session got rejected (key revoked,
  //     server restarted with a different secret, …). We surface a
  //     toast so the user understands why they're suddenly back at
  //     the login screen.
  //   - prev === null → the failure happened *during* a login attempt
  //     (the LoginPage's `ping` probe). The login form already shows
  //     an inline error and emits its own focused toast, so we stay
  //     quiet here to avoid double-announcing the same failure.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setAuth((prev) => {
        if (prev?.baseUrl) {
          saveLoginDraft({ baseUrl: prev.baseUrl, apiKey: "" });
        }
        if (prev) {
          notify.error("Session expired — please sign in again.");
        }
        return null;
      });
      clearAuth();
    });
    return () => setUnauthorizedHandler(null);
  }, [notify]);

  const value = useMemo<AuthContextValue>(
    () => ({ auth, api: auth ? makeApi(auth) : null, login, logout }),
    [auth, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}

export function useApi(): Api {
  const { api } = useAuth();
  if (!api) throw new Error("API used without auth");
  return api;
}
