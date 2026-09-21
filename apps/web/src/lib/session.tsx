import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, authEvents, getToken, setToken } from "./api";
import { closeSocket, reconnectSocket } from "./socket";
import type { Account } from "./types";

interface SessionContextValue {
  account: Account | null;
  loading: boolean;
  signup: (displayName: string, email: string, password: string) => Promise<void>;
  login: (email: string, password: string) => Promise<void>;
  logout: () => void;
  refresh: () => Promise<void>;
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [account, setAccount] = useState<Account | null>(null);
  const [loading, setLoading] = useState(true);

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
    return () => authEvents.removeEventListener("unauthenticated", onUnauthenticated);
  }, []);

  const signup = useCallback(async (displayName: string, email: string, password: string) => {
    const res = await api.post<{ token: string; account: Account }>("/api/auth/signup", {
      displayName,
      email,
      password,
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

  const logout = useCallback(() => {
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
    <SessionContext.Provider value={{ account, loading, signup, login, logout, refresh }}>
      {children}
    </SessionContext.Provider>
  );
}

export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used within SessionProvider");
  return ctx;
}
