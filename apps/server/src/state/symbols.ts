import { Exchange } from "@stockastic/matching-engine";

// TODO: rulebook TBD — symbol universe, tick sizes per symbol, etc. Stub list
// so the base terminal has something real to render against.
export const SYMBOLS = [
  { symbol: "ACME", displayName: "Acme Corp" },
  { symbol: "GLOBEX", displayName: "Globex Industries" },
  { symbol: "INITECH", displayName: "Initech" },
  { symbol: "UMBRELLA", displayName: "Umbrella Group" },
] as const;

export type SymbolCode = (typeof SYMBOLS)[number]["symbol"];

/** Single shared matching engine instance for the whole server process. */
export const exchange = new Exchange();
