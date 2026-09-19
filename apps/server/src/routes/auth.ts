import type { FastifyInstance } from "fastify";
import { z } from "zod";
import { accountStore } from "../state/accounts";
import { hashPassword, verifyPassword } from "../auth/passwords";
import { signSession } from "../auth/jwt";
import { requireAuth } from "../auth/middleware";
import { persistAccount } from "../persistence";

const signupSchema = z.object({
  displayName: z.string().min(1),
  email: z.string().email(),
  password: z.string().min(8),
});

const loginSchema = z.object({
  email: z.string().email(),
  password: z.string().min(1),
});

function toPublicAccount(account: ReturnType<typeof accountStore.get>) {
  if (!account) return null;
  const { passwordHash: _passwordHash, ...pub } = account;
  return pub;
}

export async function authRoutes(app: FastifyInstance) {
  app.post("/api/auth/signup", async (request, reply) => {
    const parsed = signupSchema.safeParse(request.body);
    if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });

    if (accountStore.getByEmail(parsed.data.email)) {
      return reply.status(409).send({ error: "email_already_registered" });
    }

    const passwordHash = await hashPassword(parsed.data.password);
    const account = accountStore.create(parsed.data.displayName, parsed.data.email, passwordHash);

    const { persisted } = await persistOrFail(account);
    const token = signSession({ sub: account.id, role: account.role, isAdmin: account.isAdmin });
    return reply.send({ token, account: toPublicAccount(account), persisted });
  });

  app.post("/api/auth/login", async (request, reply) => {
    const parsed = loginSchema.safeParse(request.body);
    if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });

    const account = accountStore.getByEmail(parsed.data.email);
    const ok = account && (await verifyPassword(parsed.data.password, account.passwordHash));
    if (!account || !ok) {
      return reply.status(401).send({ error: "invalid_credentials" });
    }

    const token = signSession({ sub: account.id, role: account.role, isAdmin: account.isAdmin });
    return reply.send({ token, account: toPublicAccount(account) });
  });

  app.get("/api/auth/me", { preHandler: requireAuth }, async (request, reply) => {
    const account = accountStore.get(request.account!.sub);
    if (!account) return reply.status(404).send({ error: "not_found" });
    return toPublicAccount(account);
  });
}

async function persistOrFail(account: NonNullable<ReturnType<typeof accountStore.get>>) {
  return persistAccount(account).then(
    () => ({ persisted: true }),
    () => ({ persisted: false })
  );
}
