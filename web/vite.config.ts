import { sveltekit } from "@sveltejs/kit/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    proxy: {
      "/api": { target: "http://localhost:8443", changeOrigin: true },
      "/health": { target: "http://localhost:8443" },
      "/ready": { target: "http://localhost:8443" },
      "/ws": { target: "ws://localhost:8443", ws: true },
    },
  },
});
