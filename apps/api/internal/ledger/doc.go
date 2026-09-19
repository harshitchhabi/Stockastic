// Package ledger owns accounts, cash, holdings and open-order reservations (no shorting,
// no leverage). Applies fills; exposes wallet/portfolio valuation. In-memory hot state that
// package store makes durable.
package ledger
