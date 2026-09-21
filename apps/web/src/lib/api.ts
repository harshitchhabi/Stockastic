// Same-origin by default: in production the Go binary serves both this bundle and /api, and in dev
// Vite proxies /api, so there is no CORS and no hard-coded host. VITE_API_URL overrides for odd setups.
export const API_BASE = (import.meta.env.VITE_API_URL as string | undefined) ?? "";

const TOKEN_KEY = "stockastic:token";
const REQUEST_TIMEOUT_MS = 15_000;

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null; // storage blocked (private mode / policy): behave as logged out, don't crash
  }
}

export function setToken(token: string | null): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
}

/** Fired when a request comes back 401 — the session provider listens for this to log out. */
export const authEvents = new EventTarget();

/** An HTTP error from the API. `status` 0 means the request never got a response (network/timeout). */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string
  ) {
    super(message);
  }
  /** True when the server never saw, or never finished, the request — safe to retry an idempotent one. */
  get transient(): boolean {
    return this.status === 0 || this.status === 502 || this.status === 503 || this.status === 504;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), REQUEST_TIMEOUT_MS);
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      ...init,
      signal: ctl.signal,
      headers: {
        // Only claim a JSON body when one is sent: a server's JSON parser rejects an empty body that
        // is labelled application/json, which silently broke every no-body POST once.
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...init?.headers,
      },
    });
  } catch (err) {
    const timedOut = err instanceof DOMException && err.name === "AbortError";
    throw new ApiError(timedOut ? "request timed out" : "network error", 0, timedOut ? "timeout" : "network");
  } finally {
    clearTimeout(timer);
  }

  if (res.status === 401) authEvents.dispatchEvent(new Event("unauthenticated"));

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const code = typeof body?.error === "string" ? body.error : undefined;
    // Prefer the server's plain-language message; the code stays available for logic.
    const message = typeof body?.message === "string" && body.message ? body.message : code;
    throw new ApiError(message ?? `request failed (${res.status})`, res.status, code);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) =>
    request<T>(path, { method: "POST", body: body ? JSON.stringify(body) : undefined }),
  put: <T>(path: string, body?: unknown) =>
    request<T>(path, { method: "PUT", body: body ? JSON.stringify(body) : undefined }),
  del: <T>(path: string) => request<T>(path, { method: "DELETE" }),

  /**
   * POST that is safe to repeat because the body carries a client-generated idempotency key (trades:
   * `clientTradeId`). On a network error, timeout or 502/503/504 it retries the SAME body, so a dropped
   * connection mid-submit can never cause a duplicate trade — the server returns the original result.
   * Real rejections (4xx, 423 frozen, 429 rate limit) are not retried.
   */
  postIdempotent: async <T>(path: string, body: unknown, attempts = 4): Promise<T> => {
    let last: unknown;
    for (let i = 0; i < attempts; i++) {
      try {
        return await request<T>(path, { method: "POST", body: JSON.stringify(body) });
      } catch (err) {
        last = err;
        if (!(err instanceof ApiError) || !err.transient) throw err;
        await sleep(400 * 2 ** i + Math.random() * 250);
      }
    }
    throw last;
  },
};
