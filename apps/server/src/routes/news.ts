import type { FastifyInstance } from "fastify";
import { newsDispatcher } from "../news/dispatcher";

export async function newsRoutes(app: FastifyInstance) {
  // Publishing is an admin/organizer action — see POST /api/admin/news.
  // This is a read-only snapshot of the public feed for a page refresh; the
  // live push still comes over the websocket 'news' event.
  app.get("/api/news", async () => {
    return newsDispatcher.list();
  });
}
