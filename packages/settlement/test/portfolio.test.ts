import { describe, expect, it } from "vitest";
import { computeFinalPortfolioValue } from "../src/portfolio";

describe("computeFinalPortfolioValue", () => {
  it("sums cash + holdings at freeze price + fund positions at final NAV", () => {
    const result = computeFinalPortfolioValue({
      cashBalance: 10_000,
      holdings: [{ symbol: "ACME", qty: 10 }],
      freezePrices: { ACME: 105 },
      fundPositions: [{ fundId: "fund-1", units: 50 }],
      fundFinalNav: { "fund-1": 2.5 },
    });

    expect(result.holdingsValue).toBe(1050);
    expect(result.fundValue).toBe(125);
    expect(result.totalValue).toBe(10_000 + 1050 + 125);
  });

  it("handles an account with no positions at all", () => {
    const result = computeFinalPortfolioValue({
      cashBalance: 5000,
      holdings: [],
      freezePrices: {},
      fundPositions: [],
      fundFinalNav: {},
    });
    expect(result.totalValue).toBe(5000);
  });

  it("throws rather than silently valuing a held symbol at 0 when its freeze price is missing", () => {
    expect(() =>
      computeFinalPortfolioValue({
        cashBalance: 0,
        holdings: [{ symbol: "GLOBEX", qty: 5 }],
        freezePrices: {},
        fundPositions: [],
        fundFinalNav: {},
      })
    ).toThrow(/GLOBEX/);
  });

  it("throws rather than silently valuing a fund position at 0 when final NAV is missing", () => {
    expect(() =>
      computeFinalPortfolioValue({
        cashBalance: 0,
        holdings: [],
        freezePrices: {},
        fundPositions: [{ fundId: "fund-x", units: 10 }],
        fundFinalNav: {},
      })
    ).toThrow(/fund-x/);
  });
});
