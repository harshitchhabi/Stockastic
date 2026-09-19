// Package auth issues and verifies JWTs (one token, shared by REST and WebSocket) and
// hashes passwords. Role is re-read from state on each request, never trusted from the token.
package auth
