// Package store is the pgx/Postgres layer: migrations, repositories, and hydration on startup.
// Write-before-ack: a mutation is acknowledged to the client only after its transaction
// commits. Idempotency keys are enforced here as well as in the engine.
package store
