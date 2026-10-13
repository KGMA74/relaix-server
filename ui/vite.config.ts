import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { defineConfig } from "vite";

// The API routes the dashboard calls. In development Vite serves the app and
// forwards these to a gatewayd running locally; in production both come from
// the same gatewayd, so the app always calls its own origin.
const apiRoutes = ["/send", "/ussd", "/inbox", "/jobs", "/devices", "/stats", "/admin", "/events", "/countries", "/ui-config", "/healthz", "/readyz"];
const target = process.env.RELAIX_API ?? "http://localhost:8080";

export default defineConfig({
  // gatewayd mounts the dashboard under /ui/, beside the API rather than over it.
  base: "/ui/",
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "src") },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: Object.fromEntries(apiRoutes.map((r) => [r, { target, changeOrigin: true }])),
  },
});
