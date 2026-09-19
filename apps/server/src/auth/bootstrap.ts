import { accountStore } from "../state/accounts";
import { hashPassword } from "./passwords";
import { persistAccount } from "../persistence";

/**
 * Ensures exactly one admin account exists, from ADMIN_EMAIL/ADMIN_PASSWORD.
 * There is no self-service admin signup — the organizer's admin credentials
 * are provisioned this way once per deployment, not created through the
 * public /api/auth/signup path.
 */
export async function bootstrapAdmin(): Promise<void> {
  const email = process.env.ADMIN_EMAIL;
  const password = process.env.ADMIN_PASSWORD;
  if (!email || !password) {
    console.warn(
      "[auth] ADMIN_EMAIL/ADMIN_PASSWORD not set — no admin account will exist until one is provisioned."
    );
    return;
  }

  let account = accountStore.getByEmail(email);
  if (!account) {
    const passwordHash = await hashPassword(password);
    account = accountStore.create("Admin", email, passwordHash);
  }
  account.isAdmin = true;
  await persistAccount(account);
  console.log(`[auth] admin account ready: ${email}`);
}
