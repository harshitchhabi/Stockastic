export interface AumSample {
  /** Epoch ms. */
  t: number;
  aum: number;
}

/**
 * Time-weighted average AUM over [periodStart, periodEnd]: each sample's AUM
 * holds until the next sample (the leaderboard's 5-minute refresh interval,
 * Sec 12), and the last sample holds until the period ends.
 */
export function timeWeightedAverageAum(
  samples: AumSample[],
  periodStart: number,
  periodEnd: number
): number {
  if (periodEnd <= periodStart) return 0;
  const inPeriod = samples
    .filter((s) => s.t <= periodEnd)
    .sort((a, b) => a.t - b.t);
  if (inPeriod.length === 0) return 0;

  // The AUM in force at periodStart is the latest sample at or before it.
  let carried = 0;
  for (const s of inPeriod) if (s.t <= periodStart) carried = s.aum;

  let weighted = 0;
  let cursor = periodStart;
  let current = carried;
  for (const s of inPeriod) {
    if (s.t <= periodStart) continue;
    weighted += current * (s.t - cursor);
    cursor = s.t;
    current = s.aum;
  }
  weighted += current * (periodEnd - cursor);
  return weighted / (periodEnd - periodStart);
}

/**
 * Sec 12 management fee for one settlement period: charged on time-weighted
 * average AUM regardless of performance. `proration` = headcount ÷ standard
 * team size for short-handed funds (Sec 6). The percent's basis (per period
 * vs per event) is CONFIG.fees.managementFeeBasis.
 */
export function managementFeeForPeriod(input: {
  samples: AumSample[];
  periodStart: number;
  periodEnd: number;
  managementFeePercent: number;
  proration?: number;
}): { averageAum: number; fee: number } {
  const averageAum = timeWeightedAverageAum(input.samples, input.periodStart, input.periodEnd);
  return {
    averageAum,
    fee: averageAum * (input.managementFeePercent / 100) * (input.proration ?? 1),
  };
}
