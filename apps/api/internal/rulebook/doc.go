// Package rulebook exposes the event-rule constants (timeline, fees, caps, prize weights,
// rate limits) to the backend. Single source of truth is packages/config; this package is
// the only place the backend reads them, so a rulebook change never touches engine or ledger code.
package rulebook
