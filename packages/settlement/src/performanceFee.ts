export interface FundCheckpoint {
  timestamp: number;
  navPerUnit: number;
  totalUnitsOutstanding: number;
}

export interface PerformanceFeeCheckpointResult {
  timestamp: number;
  navPerUnit: number;
  highWaterMarkBefore: number;
  highWaterMarkAfter: number;
  /** Fee per unit, in NAV currency, charged at this checkpoint (0 if NAV didn't set a new high). */
  feePerUnit: number;
  /** feePerUnit × totalUnitsOutstanding × proration at this checkpoint. */
  totalFeeCharged: number;
}

export interface PerformanceFeeOptions {
  /**
   * Sec 12: the fund's starting high-water mark (its launch NAV). When given,
   * even the first checkpoint can pay a fee. When omitted, the first
   * checkpoint is treated as inception and sets the mark without paying.
   */
  initialHighWaterMark?: number;
  /** Sec 6/12: short-handed funds scale the profit basis by headcount ÷ standard size. */
  proration?: number;
}

/**
 * Sec 12 PROVISIONAL performance fee: at each checkpoint, a fee is charged
 * only on NAV growth above the previous all-time-high NAV/unit; a checkpoint
 * that doesn't set a new high charges nothing and leaves the mark untouched.
 * These provisional figures are superseded at Final Settlement by
 * computeFinalPerformanceFee (fee clawback). `performanceFeePercent = null`
 * computes zero fees while still tracking the mark.
 */
export function computeHighWaterMarkFees(
  checkpoints: FundCheckpoint[],
  performanceFeePercent: number | null,
  options: PerformanceFeeOptions = {}
): PerformanceFeeCheckpointResult[] {
  if (checkpoints.length === 0) return [];

  const feeFraction = (performanceFeePercent ?? 0) / 100;
  const proration = options.proration ?? 1;
  const hasInitialMark = options.initialHighWaterMark !== undefined;
  let highWaterMark = options.initialHighWaterMark ?? checkpoints[0].navPerUnit;

  return checkpoints.map((checkpoint, index) => {
    const highWaterMarkBefore = highWaterMark;
    const canPay = hasInitialMark || index > 0;
    const gain = canPay && checkpoint.navPerUnit > highWaterMarkBefore
      ? checkpoint.navPerUnit - highWaterMarkBefore
      : 0;

    const feePerUnit = gain * feeFraction;
    if (gain > 0) highWaterMark = checkpoint.navPerUnit;

    return {
      timestamp: checkpoint.timestamp,
      navPerUnit: checkpoint.navPerUnit,
      highWaterMarkBefore,
      highWaterMarkAfter: highWaterMark,
      feePerUnit,
      totalFeeCharged: feePerUnit * checkpoint.totalUnitsOutstanding * proration,
    };
  });
}

/**
 * Sec 12 fee clawback: at Final Settlement the performance fee is recalculated
 * ONCE against the fund's sustained final NAV. A NAV peak that wasn't held to
 * the end contributes nothing, so this is never higher than the peak-based
 * provisional total (Appendix A.4: peak 121.89, final 115.80 → fee on 15.80,
 * not 21.89).
 */
export function computeFinalPerformanceFee(input: {
  launchNav: number;
  finalNav: number;
  unitsAtFinal: number;
  performanceFeePercent: number | null;
  proration?: number;
}): { feePerUnit: number; totalFee: number } {
  const gain = Math.max(0, input.finalNav - input.launchNav);
  const feePerUnit = gain * ((input.performanceFeePercent ?? 0) / 100);
  return { feePerUnit, totalFee: feePerUnit * input.unitsAtFinal * (input.proration ?? 1) };
}
