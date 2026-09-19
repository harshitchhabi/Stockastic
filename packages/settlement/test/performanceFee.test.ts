import { describe, expect, it } from "vitest";
import { computeHighWaterMarkFees } from "../src/performanceFee";

describe("computeHighWaterMarkFees", () => {
  it("charges nothing at inception", () => {
    const [first] = computeHighWaterMarkFees(
      [{ timestamp: 0, navPerUnit: 1, totalUnitsOutstanding: 100 }],
      20
    );
    expect(first.feePerUnit).toBe(0);
    expect(first.highWaterMarkAfter).toBe(1);
  });

  it("charges a fee only on the gain above the previous peak", () => {
    const results = computeHighWaterMarkFees(
      [
        { timestamp: 0, navPerUnit: 1, totalUnitsOutstanding: 100 },
        { timestamp: 1, navPerUnit: 1.5, totalUnitsOutstanding: 100 }, // new peak: gain 0.5
      ],
      20 // 20%
    );

    expect(results[1].feePerUnit).toBeCloseTo(0.1); // 0.5 * 0.20
    expect(results[1].totalFeeCharged).toBeCloseTo(10); // 0.1 * 100 units
    expect(results[1].highWaterMarkAfter).toBe(1.5);
  });

  it("charges nothing when NAV drops or merely recovers without exceeding the prior peak", () => {
    const results = computeHighWaterMarkFees(
      [
        { timestamp: 0, navPerUnit: 2, totalUnitsOutstanding: 100 },
        { timestamp: 1, navPerUnit: 1.5, totalUnitsOutstanding: 100 }, // drawdown
        { timestamp: 2, navPerUnit: 1.9, totalUnitsOutstanding: 100 }, // recovers, still below 2
      ],
      20
    );

    expect(results[1].feePerUnit).toBe(0);
    expect(results[1].highWaterMarkAfter).toBe(2); // mark unchanged on drawdown
    expect(results[2].feePerUnit).toBe(0);
    expect(results[2].highWaterMarkAfter).toBe(2);
  });

  it("charges fees again once NAV exceeds the mark a second time", () => {
    const results = computeHighWaterMarkFees(
      [
        { timestamp: 0, navPerUnit: 1, totalUnitsOutstanding: 100 },
        { timestamp: 1, navPerUnit: 1.2, totalUnitsOutstanding: 100 }, // peak -> fee
        { timestamp: 2, navPerUnit: 1.1, totalUnitsOutstanding: 100 }, // drawdown -> no fee
        { timestamp: 3, navPerUnit: 1.4, totalUnitsOutstanding: 100 }, // new peak over 1.2 -> fee on 0.2
      ],
      10
    );

    expect(results[1].feePerUnit).toBeCloseTo(0.02);
    expect(results[2].feePerUnit).toBe(0);
    expect(results[3].feePerUnit).toBeCloseTo(0.02); // 10% of (1.4 - 1.2)
    expect(results[3].highWaterMarkAfter).toBe(1.4);
  });

  it("computes zero fees when the fee percent is still TBD (null), but still tracks the high-water mark", () => {
    const results = computeHighWaterMarkFees(
      [
        { timestamp: 0, navPerUnit: 1, totalUnitsOutstanding: 100 },
        { timestamp: 1, navPerUnit: 2, totalUnitsOutstanding: 100 },
      ],
      null
    );

    expect(results[1].feePerUnit).toBe(0);
    expect(results[1].totalFeeCharged).toBe(0);
    expect(results[1].highWaterMarkAfter).toBe(2); // mark tracking is independent of the fee %
  });

  it("scales the total fee by units outstanding at that checkpoint, which may change between checkpoints", () => {
    const results = computeHighWaterMarkFees(
      [
        { timestamp: 0, navPerUnit: 1, totalUnitsOutstanding: 100 },
        { timestamp: 1, navPerUnit: 1.1, totalUnitsOutstanding: 500 }, // more units allocated by now
      ],
      10
    );

    expect(results[1].feePerUnit).toBeCloseTo(0.01);
    expect(results[1].totalFeeCharged).toBeCloseTo(5); // 0.01 * 500
  });
});
