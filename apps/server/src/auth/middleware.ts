import type { FastifyReply, FastifyRequest } from "fastify";
import { verifySession, type SessionClaims } from "./jwt";

declare module "fastify" {
  interface FastifyRequest {
    account?: SessionClaims;
  }
}

function extractToken(request: FastifyRequest): string | null {
  const header = request.headers.authorization;
  if (!header?.startsWith("Bearer ")) return null;
  return header.slice("Bearer ".length);
}

/** Fastify preHandler: verifies the bearer JWT and attaches `request.account`. */
export async function requireAuth(request: FastifyRequest, reply: FastifyReply): Promise<void> {
  const token = extractToken(request);
  const claims = token ? verifySession(token) : null;
  if (!claims) {
    reply.status(401).send({ error: "unauthenticated" });
    return reply; // fastify treats a returned reply as "handled, stop here"
  }
  request.account = claims;
}

/** Fastify preHandler: requireAuth, plus rejects non-admin accounts. */
export async function requireAdmin(request: FastifyRequest, reply: FastifyReply): Promise<void> {
  await requireAuth(request, reply);
  if (reply.sent) return;
  if (!request.account?.isAdmin) {
    reply.status(403).send({ error: "admin_only" });
  }
}
