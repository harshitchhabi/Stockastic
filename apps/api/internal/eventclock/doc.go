// Package eventclock is the event state machine: the 17-block, 300-minute timeline,
// phases, market open/closed, allocation windows 0-3, freeze snapshots, pause/resume, and
// admin overrides. Emits block-change events; everything time-dependent asks it.
package eventclock
