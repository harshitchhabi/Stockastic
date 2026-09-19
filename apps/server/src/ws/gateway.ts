import type { Server as HttpServer } from "node:http";
import { Server as SocketIoServer } from "socket.io";
import { exchange } from "../state/symbols";
import { marketData } from "../state/market";
import { newsDispatcher } from "../news/dispatcher";
import { controlState, controlEvents } from "../state/controlState";
import { verifySession } from "../auth/jwt";

/**
 * All live data (book depth, fills, news, control-state changes) flows over
 * Socket.io. REST is for request/response actions (submit/cancel order,
 * fetch snapshots); the websocket is the push channel. Clients join a room
 * per symbol so a ~750 concurrent-client event doesn't broadcast every tick
 * to every socket.
 *
 * Identity is established once, from the same JWT used for REST, in the
 * connection handshake — never from a client-supplied accountId in an
 * 'identify' event, which would let any socket impersonate any account's
 * private room. A dropped-and-reconnected socket re-authenticates the same
 * way; the client is expected to re-fetch REST snapshots (depth, portfolio,
 * pending orders) on every 'connect' event rather than trust stale state
 * carried across the drop.
 */
export function attachWebsocketGateway(httpServer: HttpServer) {
  const io = new SocketIoServer(httpServer, {
    cors: { origin: "*" }, // TODO: lock down once deployment origin is known
  });

  io.use((socket, next) => {
    const token = socket.handshake.auth?.token as string | undefined;
    const claims = token ? verifySession(token) : null;
    if (!claims) {
      next(new Error("unauthenticated"));
      return;
    }
    socket.data.account = claims;
    next();
  });

  exchange.on("bookUpdate", (depth) => {
    io.to(`symbol:${depth.symbol}`).emit("bookUpdate", depth);
  });

  exchange.on("fill", (fill) => {
    marketData.recordTrade(fill.symbol, fill.price, fill.timestamp);
    io.to(`symbol:${fill.symbol}`).emit("trade", fill);

    // Fills also affect the two accounts directly involved — notify their
    // private rooms so portfolio/leaderboard panels can refresh.
    io.to(`account:${fill.takerAccountId}`).emit("fill", fill);
    io.to(`account:${fill.makerAccountId}`).emit("fill", fill);
  });

  exchange.on("orderAccepted", (order) => {
    io.to(`account:${order.accountId}`).emit("orderAccepted", order);
  });

  exchange.on("orderCancelled", (order) => {
    io.to(`account:${order.accountId}`).emit("orderCancelled", order);
  });

  newsDispatcher.on("fundManagerNews", (item) => {
    io.to("role:fund_manager").emit("news", item);
  });
  newsDispatcher.on("publicNews", (item) => {
    io.emit("news", item); // public feed: everyone, including fund managers who already saw it
  });

  // Admin actions (freeze/unfreeze, window overrides) push instantly to every
  // connected client so the UI reflects a halt/window change without a poll.
  controlEvents.on("change", (snapshot) => {
    io.emit("controlState", snapshot);
  });

  io.on("connection", (socket) => {
    const account = socket.data.account as { sub: string; role: string };
    socket.join(`account:${account.sub}`);
    if (account.role === "fund_manager") {
      socket.join("role:fund_manager");
    }

    // Tell the freshly (re)connected client the current control state right
    // away, rather than waiting for the next change to happen to broadcast.
    socket.emit("controlState", controlState.snapshot());

    socket.on("subscribe:symbol", (symbol: string) => {
      socket.join(`symbol:${symbol}`);
    });
    socket.on("unsubscribe:symbol", (symbol: string) => {
      socket.leave(`symbol:${symbol}`);
    });
  });

  return io;
}
