/** Reads a CSS variable so canvas-drawn parts (the chart) use the same palette as the page. */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}
