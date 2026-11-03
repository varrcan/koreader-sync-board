import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Сборка кладётся туда, откуда Go-сервер вшивает SPA через embed.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../../server/internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
