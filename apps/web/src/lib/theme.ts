// Light "paper" and dark "night desk" themes. The choice is a per-viewer convenience: storage may be
// unavailable, so every access is guarded and the page works without it.
export type Theme = "light" | "dark";

const KEY = "stockastic.theme";

export function currentTheme(): Theme {
  const set = document.documentElement.dataset.theme;
  if (set === "light" || set === "dark") return set;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function applyStoredTheme(): void {
  try {
    const t = localStorage.getItem(KEY);
    if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  } catch {
    /* no storage: follow the OS */
  }
}

export function toggleTheme(): Theme {
  const next: Theme = currentTheme() === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  try {
    localStorage.setItem(KEY, next);
  } catch {
    /* ignore */
  }
  window.dispatchEvent(new Event("themechange"));
  return next;
}

/** Reads a CSS variable so canvas-drawn parts (the chart) follow the theme. */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}
