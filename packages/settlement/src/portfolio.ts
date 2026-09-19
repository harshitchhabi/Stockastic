export interface HoldingPosition {
  symbol: string;
  qty: number;
}

export interface FundPosition {
  fundId: string;
  units: number;
}

export interface FinalPortfolioInput {
  cashBalance: number;
  holdings: HoldingPosition[];
  /** Symbol -> price at the moment trading was frozen. Must cover every held symbol. */
  freezePrices: Record<string, number>;
  fundPositions: FundPosition[];
  /** Fund id -> final NAV/unit at settlement. Must cover every fund position held. */
  fundFinalNav: Record<string, number>;
}

export interface HoldingValuation extends HoldingPosition {
  freezePrice: number;
  value: number;
}

export interface FundValuation extends FundPosition {
  finalNavPerUnit: number;
  value: number;
}

export interface FinalPortfolioResult {
  cash: number;
  holdingsValue: number;
  fundValue: number;
  /** cash + holdings at freeze price + (fund units × final NAV). */
  totalValue: number;
  holdings: HoldingValuation[];
  funds: FundValuation[];
}

/**
 * Final portfolio value at settlement: cash + holdings marked at the freeze
 * price + fund positions marked at final NAV. The formula shape is fixed by
 * the rulebook's definition of "final value" and won't change; only the
 * freeze prices / final NAVs feeding it are determined live at event end.
 *
 * Throws rather than silently valuing at 0 if a held symbol/fund is missing
 * its freeze price / final NAV — a missing settlement price is a data-
 * integrity bug, not a zero position, and must not be allowed to pass
 * ownership-record accuracy under a "trades are final" rule.
 */
export function computeFinalPortfolioValue(input: FinalPortfolioInput): FinalPortfolioResult {
  const holdings: HoldingValuation[] = input.holdings.map((h) => {
    const freezePrice = input.freezePrices[h.symbol];
    if (freezePrice === undefined) {
      throw new Error(`missing freeze price for symbol "${h.symbol}"`);
    }
    return { ...h, freezePrice, value: freezePrice * h.qty };
  });

  const funds: FundValuation[] = input.fundPositions.map((f) => {
    const finalNavPerUnit = input.fundFinalNav[f.fundId];
    if (finalNavPerUnit === undefined) {
      throw new Error(`missing final NAV for fund "${f.fundId}"`);
    }
    return { ...f, finalNavPerUnit, value: finalNavPerUnit * f.units };
  });

  const holdingsValue = holdings.reduce((sum, h) => sum + h.value, 0);
  const fundValue = funds.reduce((sum, f) => sum + f.value, 0);

  return {
    cash: input.cashBalance,
    holdingsValue,
    fundValue,
    totalValue: input.cashBalance + holdingsValue + fundValue,
    holdings,
    funds,
  };
}
