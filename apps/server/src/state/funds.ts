import { randomUUID } from "node:crypto";

/**
 * Phase 2 stub. Field set (pitch/NAV/risk-profile) is explicitly TBD per the
 * rulebook — this is only structural scaffolding so the fund browser and
 * fund-manager panels have real data to render, not the final shape.
 */
export interface Fund {
  id: string;
  managerAccountId: string;
  name: string;
  pitch: string;
  riskProfile: string; // TODO: rulebook TBD, likely an enum eventually
  navPerUnit: number;
  totalUnits: number;
}

export interface FundInvestment {
  id: string;
  fundId: string;
  investorId: string;
  units: number;
  action: "allocate" | "redeem";
  createdAt: number;
}

class FundStore {
  private readonly funds = new Map<string, Fund>();
  private readonly investments: FundInvestment[] = [];

  create(managerAccountId: string, name: string, pitch: string, riskProfile: string): Fund {
    const fund: Fund = {
      id: randomUUID(),
      managerAccountId,
      name,
      pitch,
      riskProfile,
      navPerUnit: 1,
      totalUnits: 0,
    };
    this.funds.set(fund.id, fund);
    return fund;
  }

  list(): Fund[] {
    return [...this.funds.values()];
  }

  get(id: string): Fund | undefined {
    return this.funds.get(id);
  }

  setNav(fundId: string, navPerUnit: number): Fund | undefined {
    const fund = this.funds.get(fundId);
    if (!fund) return undefined;
    fund.navPerUnit = navPerUnit;
    return fund;
  }

  record(fundId: string, investorId: string, units: number, action: "allocate" | "redeem"): FundInvestment {
    const fund = this.funds.get(fundId);
    if (fund) {
      fund.totalUnits += action === "allocate" ? units : -units;
    }
    const entry: FundInvestment = { id: randomUUID(), fundId, investorId, units, action, createdAt: Date.now() };
    this.investments.push(entry);
    return entry;
  }

  breakdownForFund(fundId: string) {
    const byInvestor = new Map<string, number>();
    for (const inv of this.investments.filter((i) => i.fundId === fundId)) {
      const delta = inv.action === "allocate" ? inv.units : -inv.units;
      byInvestor.set(inv.investorId, (byInvestor.get(inv.investorId) ?? 0) + delta);
    }
    return [...byInvestor.entries()].map(([investorId, units]) => ({ investorId, units }));
  }
}

export const fundStore = new FundStore();
