import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, authEvents, getToken, setToken, TOKEN_KEY } from "./api";
import { closeSocket, reconnectSocket } from "./socket";
import type { Account } from "./types";

/** Someone Google has just confirmed who has no account yet: they choose to create a team or join one. */
export interface Onboarding {
  token: string;
  email: string;
  name: string;
}

const ONBOARD_KEY = "stockastic.onboard";

function readPass(token: string): Onboarding | null {
  try {
    const head = token.split(".")[0].replace(/-/g, "+").replace(/_/g, "/");
    const p = JSON.parse(decodeURIComponent(escape(atob(head)))) as { e: string; n: string; x: number };
    if (!p.e || p.x * 1000 < Date.now()) return null;
    return { token, email: p.e, name: p.n };
  } catch {
    return null;
  }
}

interface SessionContextValue {
  onboarding: Onboarding | null;
  onboard: (action: "create" | "join", value: string, eventCode?: string) => Promise<void>;
  cancelOnboarding: () => void;
  account: Account | null;
  loading: boolean;
  signup: (displayName: string, email: string, password: string, eventCode?: string, yourName?: string) => Promise<void>;
  login: (email: string, password: string) => Promise<void>;
  join: (teamCode: string, name: string, email: string, password: string, eventCode?: string) => Promise<void>;
  logout: () => void;
  refresh: () => Promise<void>;
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [account, setAccount] = useState<Account | null>(null);
  const [loading, setLoading] = useState(true);
  const [onboarding, setOnboarding] = useState<Onboarding | null>(() => {
    try {
      const saved = sessionStorage.getItem(ONBOARD_KEY);
      return saved ? readPass(saved) : null;
    } catch {
      return null;
    }
  });

  const loadMe = useCallback(async () => {
    try {
      const acc = await api.get<Account>("/api/auth/me");
      setAccount(acc);
    } catch {
      setToken(null);
      setAccount(null);
    }
  }, []);

  useEffect(() => {
    // Coming back from "Continue with Google": the server put our login in the part of the address after #.
    // Keep it, and take it out of the address bar straight away.
    const h = window.location.hash;
    if (h.startsWith("#/onboard=")) {
      const pass = readPass(decodeURIComponent(h.slice("#/onboard=".length)));
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
      setOnboarding(pass);
      try {
        if (pass) sessionStorage.setItem(ONBOARD_KEY, pass.token);
      } catch {
        /* ignore */
      }
    }
    if (h.startsWith("#/signin=")) {
      setToken(decodeURIComponent(h.slice("#/signin=".length)));
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
    }
    if (getToken()) {
      loadMe().finally(() => setLoading(false));
    } else {
      setLoading(false);
    }
  }, [loadMe]);

  useEffect(() => {
    const onUnauthenticated = () => {
      closeSocket();
      setToken(null);
      setAccount(null);
    };
    authEvents.addEventListener("unauthenticated", onUnauthenticated);
    // Another tab of this browser signed out, or signed in as someone else: follow it, so no tab stays signed in
    // (with a live connection) as someone who has left.
    const onStorage = (e: StorageEvent) => {
      if (e.key !== TOKEN_KEY && e.key !== null) return;
      if (!e.newValue) {
        closeSocket();
        setAccount(null);
      } else if (e.newValue !== e.oldValue) {
        window.location.reload();
      }
    };
    window.addEventListener("storage", onStorage);
    return () => {
      authEvents.removeEventListener("unauthenticated", onUnauthenticated);
      window.removeEventListener("storage", onStorage);
    };
  }, []);

  const signup = useCallback(async (displayName: string, email: string, password: string, eventCode?: string, yourName?: string) => {
    const res = await api.post<{ token: string; account: Account }>("/api/auth/signup", {
      displayName,
      email,
      password,
      eventCode,
      yourName,
    });
    setToken(res.token);
    setAccount(res.account);
    reconnectSocket();
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    const res = await api.post<{ token: string; account: Account }>("/api/auth/login", {
      email,
      password,
    });
    setToken(res.token);
    setAccount(res.account);
    reconnectSocket();
  }, []);

  const join = useCallback(async (teamCode: string, name: string, email: string, password: string, eventCode?: string) => {
    const res = await api.post<{ token: string; account: Account }>("/api/auth/join", { teamCode, name, email, password, eventCode });
    setToken(res.token);
    setAccount(res.account);
    reconnectSocket();
  }, []);

  const cancelOnboarding = useCallback(() => {
    setOnboarding(null);
    try {
      sessionStorage.removeItem(ONBOARD_KEY);
    } catch {
      /* ignore */
    }
  }, []);

  const onboard = useCallback(
    async (action: "create" | "join", value: string, eventCode?: string) => {
      if (!onboarding) return;
      const res = await api.post<{ token: string; account: Account }>("/api/auth/onboard", {
        token: onboarding.token,
        action,
        teamName: action === "create" ? value : undefined,
        teamCode: action === "join" ? value : undefined,
        eventCode,
      });
      setToken(res.token);
      setAccount(res.account);
      cancelOnboarding();
      reconnectSocket();
    },
    [onboarding, cancelOnboarding]
  );

  const logout = useCallback(() => {
    // Cancel this sign-in on the server too (it reads the token now, before it is cleared below). If the server
    // cannot be reached, the browser still forgets the sign-in.
    if (getToken()) void api.post("/api/auth/logout").catch(() => {});
    closeSocket();
    setToken(null);
    setAccount(null);
  }, []);

  // A refresh after a trade or a role change must never sign anyone out because of a hiccup: only a 401
  // does that (the api client handles it), so any other failure just keeps what is on screen.
  const refresh = useCallback(async () => {
    try {
      setAccount(await api.get<Account>("/api/auth/me"));
    } catch {
      /* keep the current account */
    }
  }, []);

  return (
    <SessionContext.Provider value={{ onboarding, onboard, cancelOnboarding, account, loading, signup, login, join, logout, refresh }}>
      {children}
    </SessionContext.Provider>
  );
}

export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used within SessionProvider");
  return ctx;
}
