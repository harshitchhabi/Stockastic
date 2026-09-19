import { exchange } from "./symbols";
import { accountStore } from "./accounts";
import { orderIndex } from "./orderIndex";

/** Wires matching-engine fill events into the account ledger. Call once at startup. */
export function initLedgerListener() {
  orderIndex.init();

  exchange.on("fill", (fill) => {
    accountStore.applyFill(fill.takerAccountId, fill.symbol, fill.takerSide, fill.price, fill.qty);
    const makerSide = fill.takerSide === "buy" ? "sell" : "buy";
    accountStore.applyFill(fill.makerAccountId, fill.symbol, makerSide, fill.price, fill.qty);
  });
}
