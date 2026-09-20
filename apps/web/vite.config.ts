import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";

// The Go API listens on :8080. In dev, Vite proxies /api and /ws to it so the browser sees ONE
// origin, exactly as in production where Go serves this bundle itself (no CORS anywhere).
const API = "http://127.0.0.1:8080";

// The build goes straight into the Go module, where webui embeds it (go:embed). Vite empties that
// directory on each build, so a tiny plugin restores the tracked placeholder that keeps `go build`
// working on a fresh clone before the frontend has ever been built.
const EMBED_DIR = new URL("../api/internal/webui/dist/", import.meta.url);
const keepEmbedDir = {
  name: "keep-embed-dir",
  closeBundle() {
    writeFileSync(new URL(".gitkeep", EMBED_DIR), "");
  },
};

export default defineConfig({
  plugins: [react(), keepEmbedDir],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    port: 3000,
    proxy: {
      "/api": API,
      "/ws": { target: API.replace("http", "ws"), ws: true },
    },
  },
  build: {
    outDir: fileURLToPath(EMBED_DIR),
    emptyOutDir: true,
    sourcemap: false,
    target: "es2020",
    chunkSizeWarningLimit: 700,
  },
  test: { environment: "node", include: ["src/**/*.test.ts"] },
});
