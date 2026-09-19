import { io, type Socket } from "socket.io-client";
import { API_BASE, getToken } from "./api";

let socket: Socket | null = null;

/**
 * Single shared socket for the whole tab. `auth` is a function (not a plain
 * object) so socket.io-client re-evaluates it on every connection attempt —
 * including an automatic reconnect after a dropped wifi — and always sends
 * the current JWT rather than one captured at first connect.
 */
export function getSocket(): Socket {
  if (!socket) {
    socket = io(API_BASE, {
      transports: ["websocket"],
      autoConnect: true,
      auth: (cb) => cb({ token: getToken() }),
    });
  }
  return socket;
}

/** Force a fresh handshake with the current token — call right after login/signup. */
export function reconnectSocket(): void {
  if (socket?.connected) {
    socket.disconnect().connect();
  }
}
