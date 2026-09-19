// Package config holds RUNTIME configuration only: listen ports, DATABASE_URL, JWT secret,
// pool sizes, log level, timeouts. Loaded from the environment. It never holds rulebook
// values — those live in packages/config and are read through package rulebook.
package config
