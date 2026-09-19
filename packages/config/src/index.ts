import rulebookJson from "../rulebook.json";
import type { Rulebook } from "./rulebook.generated";

export type { Rulebook };
export type EventBlock = Rulebook["event"]["timeline"][number];
export type Role = Rulebook["roles"][number];
export type Stage = EventBlock["stage"];

/** Rulebook data, straight from rulebook.json (the same file the Go backend loads). */
export const RULEBOOK = rulebookJson as unknown as Rulebook;
