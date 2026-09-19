import jwt from "jsonwebtoken";
import type { Role } from "@stockastic/config";

export interface SessionClaims {
  sub: string; // accountId
  role: Role;
  isAdmin: boolean;
}

// TODO: real deployment must set JWT_SECRET — this fallback exists only so
// `npm run dev:server` works out of the box; it is deliberately unfit to
// trust with a live event (any two processes with different auto-generated
// fallbacks would reject each other's tokens, which itself catches a
// misconfigured deploy quickly rather than silently limping along).
const SECRET = process.env.JWT_SECRET;
let warned = false;
function secret(): string {
  if (SECRET) return SECRET;
  if (!warned) {
    warned = true;
    // eslint-disable-next-line no-console
    console.warn(
      "[auth] JWT_SECRET not set — using an insecure dev-only fallback. Set JWT_SECRET before any real event."
    );
  }
  return "dev-insecure-fallback-secret-do-not-use-in-production";
}

// A 5-10 hour live event with flaky wifi should not force re-logins mid-event.
const TOKEN_TTL = "24h";

export function signSession(claims: SessionClaims): string {
  return jwt.sign(claims, secret(), { expiresIn: TOKEN_TTL });
}

export function verifySession(token: string): SessionClaims | null {
  try {
    return jwt.verify(token, secret()) as SessionClaims;
  } catch {
    return null;
  }
}
