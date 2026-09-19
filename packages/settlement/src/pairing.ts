export interface FundPairing<T> {
  /** Sec 6: Fund Team numbering follows pairing order (1..N), independent of chosen fund name. */
  fundNumber: number;
  stronger: T;
  weaker: T;
}

/**
 * Sec 6 mirror ("snake") pairing: Fund Team k merges Phase-1 Rank k with
 * Phase-1 Rank (N + 1 − k). Input must already be sorted best-first and
 * contain an even number of qualifiers.
 */
export function mirrorPairing<T>(qualifiersBestFirst: T[]): FundPairing<T>[] {
  const n = qualifiersBestFirst.length;
  if (n === 0 || n % 2 !== 0) {
    throw new Error(`mirror pairing needs a non-zero even number of qualifiers, got ${n}`);
  }
  const pairs: FundPairing<T>[] = [];
  for (let k = 0; k < n / 2; k++) {
    pairs.push({
      fundNumber: k + 1,
      stronger: qualifiersBestFirst[k],
      weaker: qualifiersBestFirst[n - 1 - k],
    });
  }
  return pairs;
}
