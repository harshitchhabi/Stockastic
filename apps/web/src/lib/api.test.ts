import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";

afterEach(() => vi.unstubAllGlobals());

function respond(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })));
}

describe("api errors", () => {
  it("shows the server's plain-language message and keeps the code", async () => {
    respond(422, { error: "insufficient_cash", message: "Not enough cash for this order." });
    const err = (await api.get("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe("Not enough cash for this order.");
    expect(err.code).toBe("insufficient_cash");
    expect(err.status).toBe(422);
  });

  it("falls back to the code when there is no message", async () => {
    respond(409, { error: "order_already_closed" });
    const err = (await api.get("/api/x").catch((e: unknown) => e)) as ApiError;
    expect(err.message).toBe("order_already_closed");
  });

  it("does not retry a real rejection", async () => {
    respond(429, { error: "rate_limited", message: "Slow down." });
    const spy = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
    await expect(api.postIdempotent("/api/orders", { clientOrderId: "1" })).rejects.toThrow("Slow down.");
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("retries the same body after a transient failure", async () => {
    let calls = 0;
    vi.stubGlobal("fetch", vi.fn(async () => (++calls < 2 ? new Response("{}", { status: 503 }) : new Response(JSON.stringify({ ok: true }), { status: 200 }))));
    await expect(api.postIdempotent("/api/orders", { clientOrderId: "1" })).resolves.toEqual({ ok: true });
    expect(calls).toBe(2);
  });
});
