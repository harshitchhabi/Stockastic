// Package engine is the matching engine: one goroutine per symbol owning that symbol's
// order book, fed by a buffered command channel (strict sequential processing per symbol).
// Price-time priority, partial fills, freeze, cancel. Each goroutine recovers its own panics.
// Knows nothing about fees, caps, windows, or persistence.
package engine
