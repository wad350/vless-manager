import type {
  BandwidthLimiter,
  BandwidthLimiterCreate,
  BandwidthLimiterUpdate,
  ConnectionLimiter,
  ConnectionLimiterCreate,
  ConnectionLimiterUpdate,
  CountResponse,
  Listable,
  Node,
  NodeCreate,
  NodeStatus,
  NodeUpdate,
  RateLimiter,
  RateLimiterCreate,
  RateLimiterUpdate,
  Squad,
  SquadCreate,
  SquadUpdate,
  TrafficLimiter,
  TrafficLimiterCreate,
  TrafficLimiterUpdate,
  User,
  UserCreate,
  UserUpdate,
  VersionInfo,
} from "./types";

export class ApiError extends Error {
  status: number;
  body: string;
  constructor(status: number, body: string) {
    super(`HTTP ${status}: ${body || "(empty)"}`);
    this.status = status;
    this.body = body;
  }
}

// UnauthorizedError is thrown for 401 responses so callers can clear stored
// credentials and redirect to the login screen.
export class UnauthorizedError extends ApiError {
  constructor(body: string) {
    super(401, body);
    this.name = "UnauthorizedError";
  }
}

export interface AuthInfo {
  baseUrl: string;
  apiKey: string;
}

const STORAGE_KEY = "sing-box-admin:auth";

export function loadAuth(): AuthInfo | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<AuthInfo>;
    if (!parsed.baseUrl || !parsed.apiKey) return null;
    return { baseUrl: parsed.baseUrl, apiKey: parsed.apiKey };
  } catch {
    return null;
  }
}

// All write paths are wrapped in try/catch so a private-mode browser, a
// disabled storage backend, or a quota error never bubbles up into the
// React effects that call us. The reads above already handle missing /
// malformed entries; failing silently here is the symmetrical behaviour.
export function saveAuth(auth: AuthInfo) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(auth));
  } catch {
    /* ignore */
  }
}

export function clearAuth() {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* ignore */
  }
}

// Login form draft — what the user typed before clicking Sign in. Stored
// independently of `AuthInfo` so it survives logout (and tab close), letting
// users come back to their last typed credentials.
export interface LoginDraft {
  baseUrl: string;
  apiKey: string;
}

const DRAFT_KEY = "sing-box-admin:login-draft";

export function loadLoginDraft(): LoginDraft {
  try {
    const raw = localStorage.getItem(DRAFT_KEY);
    if (!raw) return { baseUrl: "", apiKey: "" };
    const parsed = JSON.parse(raw) as Partial<LoginDraft>;
    return {
      baseUrl: typeof parsed.baseUrl === "string" ? parsed.baseUrl : "",
      apiKey: typeof parsed.apiKey === "string" ? parsed.apiKey : "",
    };
  } catch {
    return { baseUrl: "", apiKey: "" };
  }
}

export function saveLoginDraft(draft: LoginDraft) {
  try {
    // Drop entirely empty drafts so we don't litter localStorage with "{}".
    if (!draft.baseUrl && !draft.apiKey) {
      localStorage.removeItem(DRAFT_KEY);
      return;
    }
    localStorage.setItem(DRAFT_KEY, JSON.stringify(draft));
  } catch {
    /* ignore */
  }
}

export function clearLoginDraft() {
  try {
    localStorage.removeItem(DRAFT_KEY);
  } catch {
    /* ignore */
  }
}

// onUnauthorized lets the auth context plug in a global 401 handler that
// clears local credentials and bounces the user back to the login screen.
let onUnauthorized: ((err: UnauthorizedError) => void) | null = null;
export function setUnauthorizedHandler(fn: ((err: UnauthorizedError) => void) | null) {
  onUnauthorized = fn;
}

function joinUrl(base: string, path: string): string {
  const trimmed = base.replace(/\/+$/, "");
  const suffix = path.startsWith("/") ? path : `/${path}`;
  return `${trimmed}/manager/v1${suffix}`;
}

function buildQuery(params?: Listable): string {
  if (!params) return "";
  const segments: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    if (Array.isArray(v)) {
      const items: string[] = [];
      for (const item of v) {
        if (item === undefined || item === null || item === "") continue;
        items.push(encodeURIComponent(String(item)));
      }
      if (items.length === 0) continue;
      segments.push(`${encodeURIComponent(k)}=${items.join(",")}`);
    } else {
      segments.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
    }
  }
  return segments.length > 0 ? `?${segments.join("&")}` : "";
}

async function request<T>(
  auth: AuthInfo,
  method: string,
  path: string,
  body?: unknown,
  query?: Listable,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(joinUrl(auth.baseUrl, path) + buildQuery(query), {
      method,
      headers: {
        Authorization: `Bearer ${auth.apiKey}`,
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw new ApiError(0, e instanceof Error ? e.message : String(e));
  }
  if (res.status === 401) {
    const text = await res.text().catch(() => "");
    const err = new UnauthorizedError(text);
    if (onUnauthorized) onUnauthorized(err);
    throw err;
  }
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new ApiError(res.status, text);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!text) return undefined as T;
  // Reject HTML payloads up-front: the manager-api always returns JSON for
  // success, so anything that smells like HTML must be a misrouted request
  // (e.g. SPA fallback) and would only confuse callers expecting JSON.
  const trimmed = text.trim();
  if (trimmed.startsWith("<")) {
    throw new ApiError(
      res.status,
      "expected JSON, got HTML — is the API base URL pointing at the manager-api server?",
    );
  }
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new ApiError(res.status, `invalid JSON response: ${text.slice(0, 200)}`);
  }
}

interface CRUD<TEntity, TCreate, TUpdate, TID = number> {
  list: (q?: Listable) => Promise<TEntity[]>;
  count: (q?: Listable) => Promise<number>;
  get: (id: TID) => Promise<TEntity>;
  create: (body: TCreate) => Promise<TEntity>;
  update: (id: TID, body: TUpdate) => Promise<TEntity>;
  remove: (id: TID) => Promise<TEntity>;
}

function intCRUD<TEntity, TCreate, TUpdate>(
  auth: AuthInfo,
  base: string,
): CRUD<TEntity, TCreate, TUpdate, number> {
  return {
    list: async (q) => {
      const r = await request<TEntity[]>(auth, "GET", base, undefined, q);
      return Array.isArray(r) ? r : [];
    },
    count: async (q) => {
      const r = await request<CountResponse>(auth, "GET", `${base}/count`, undefined, q);
      return r?.count ?? 0;
    },
    get: (id) => request<TEntity>(auth, "GET", `${base}/${id}`),
    create: (body) => request<TEntity>(auth, "POST", base, body),
    update: (id, body) => request<TEntity>(auth, "PUT", `${base}/${id}`, body),
    remove: (id) => request<TEntity>(auth, "DELETE", `${base}/${id}`),
  };
}

export function makeApi(auth: AuthInfo) {
  const nodesBase = "/nodes";
  return {
    auth,
    squads: intCRUD<Squad, SquadCreate, SquadUpdate>(auth, "/squads"),
    users: intCRUD<User, UserCreate, UserUpdate>(auth, "/users"),
    bandwidthLimiters: intCRUD<BandwidthLimiter, BandwidthLimiterCreate, BandwidthLimiterUpdate>(
      auth,
      "/bandwidth-limiters",
    ),
    trafficLimiters: {
      ...intCRUD<TrafficLimiter, TrafficLimiterCreate, TrafficLimiterUpdate>(
        auth,
        "/traffic-limiters",
      ),
      // updateUsed overwrites the running raw_used counter on a
      // traffic limiter. Passing 0 is the supported "reset traffic"
      // operation surfaced in the UI; any uint64 also works for ad-hoc
      // adjustments.
      updateUsed: (id: number, used: number) =>
        request<TrafficLimiter>(
          auth,
          "PUT",
          `/traffic-limiters/${id}/used`,
          { used },
        ),
    },
    connectionLimiters: intCRUD<ConnectionLimiter, ConnectionLimiterCreate, ConnectionLimiterUpdate>(
      auth,
      "/connection-limiters",
    ),
    rateLimiters: intCRUD<RateLimiter, RateLimiterCreate, RateLimiterUpdate>(
      auth,
      "/rate-limiters",
    ),
    // Identity probe: returns the running sing-box build's version
    // and the project home URL. Used by the sidebar's About popover
    // so the panel can show what release the connected manager-api
    // is built from. Cheap, single-shot — call it once on Layout
    // mount and cache the result for the session.
    version: () => request<VersionInfo>(auth, "GET", "/version"),
    nodes: {
      list: async (q?: Listable) => {
        const r = await request<Node[]>(auth, "GET", nodesBase, undefined, q);
        return Array.isArray(r) ? r : [];
      },
      count: async (q?: Listable) => {
        const r = await request<CountResponse>(auth, "GET", `${nodesBase}/count`, undefined, q);
        return r?.count ?? 0;
      },
      get: (uuid: string) => request<Node>(auth, "GET", `${nodesBase}/${uuid}`),
      create: (body: NodeCreate) => request<Node>(auth, "POST", nodesBase, body),
      update: (uuid: string, body: NodeUpdate) =>
        request<Node>(auth, "PUT", `${nodesBase}/${uuid}`, body),
      remove: (uuid: string) => request<Node>(auth, "DELETE", `${nodesBase}/${uuid}`),
      status: async (uuid: string): Promise<NodeStatus> => {
        const r = await request<{ status: NodeStatus }>(
          auth,
          "GET",
          `${nodesBase}/${uuid}/status`,
        );
        return r.status;
      },
    },
  };
}

export type Api = ReturnType<typeof makeApi>;

export async function ping(auth: AuthInfo): Promise<void> {
  // Cheap reachability + auth check.
  await request<CountResponse>(auth, "GET", "/squads/count");
}
