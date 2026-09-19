/** Last-traded-price tracker per symbol, used for mark-to-market P&L and charting. */
class MarketData {
  private readonly lastPrice = new Map<string, number>();
  private readonly history = new Map<string, { price: number; timestamp: number }[]>();

  recordTrade(symbol: string, price: number, timestamp: number) {
    this.lastPrice.set(symbol, price);
    const series = this.history.get(symbol) ?? [];
    series.push({ price, timestamp });
    if (series.length > 5000) series.shift(); // cap in-memory history for a long-running event
    this.history.set(symbol, series);
  }

  getLastPrice(symbol: string): number | undefined {
    return this.lastPrice.get(symbol);
  }

  getHistory(symbol: string, limit = 500) {
    const series = this.history.get(symbol) ?? [];
    return series.slice(-limit);
  }
}

export const marketData = new MarketData();
