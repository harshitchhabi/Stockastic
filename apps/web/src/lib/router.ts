import { useEffect, useState } from "react";

// Hash routing: no router library, and the browser's back button works between pages.
export type Page = "explore" | "watchlist" | "holdings" | "funds" | "desk" | "standings" | "company";

export interface Route {
  page: Page;
  symbol?: string;
}

const PAGES: Page[] = ["explore", "watchlist", "holdings", "funds", "desk", "standings"];

export function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  if (parts[0] === "company" && parts[1]) return { page: "company", symbol: decodeURIComponent(parts[1]) };
  const page = PAGES.find((p) => p === parts[0]);
  return { page: page ?? "explore" };
}

export const companyPath = (symbol: string) => `#/company/${encodeURIComponent(symbol)}`;
export const pagePath = (page: Page) => `#/${page}`;

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseHash(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parseHash(window.location.hash));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return route;
}
