export type OrderSide = "buy" | "sell";

export type OrderStatus = "open" | "partially_filled" | "filled" | "cancelled" | "rejected";

export interface NewOrderRequest {
  /** Client-generated idempotency key. Same (accountId, clientOrderId) pair is deduped. */
  clientOrderId: string;
  accountId: string;
  symbol: string;
  side: OrderSide;
  /** Limit price. Market orders are not in scope for Phase 1. */
  price: number;
  qty: number;
}

export interface Order {
  /** Server-assigned unique id. */
  id: string;
  clientOrderId: string;
  accountId: string;
  symbol: string;
  side: OrderSide;
  price: number;
  qty: number;
  remainingQty: number;
  status: OrderStatus;
  /** Wall-clock time the order was accepted, for display purposes. */
  createdAt: number;
  /** Monotonic per-symbol sequence number — the authoritative time-priority tiebreak. */
  seq: number;
}

export interface Fill {
  id: string;
  symbol: string;
  price: number;
  qty: number;
  takerOrderId: string;
  makerOrderId: string;
  takerAccountId: string;
  makerAccountId: string;
  takerSide: OrderSide;
  timestamp: number;
}

export interface PriceLevel {
  price: number;
  qty: number;
  orderCount: number;
}

export interface BookDepth {
  symbol: string;
  bids: PriceLevel[];
  asks: PriceLevel[];
}

export interface SubmitOrderResult {
  order: Order;
  fills: Fill[];
  /** True if this result was served from the idempotency cache rather than freshly processed. */
  deduped: boolean;
}

export interface CancelOrderResult {
  order: Order | null;
  cancelled: boolean;
  reason?: string;
}
