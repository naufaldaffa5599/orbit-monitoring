import path from "node:path"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

// Two entry points, not a client-side router: the terminal is opened with
// window.open() from the dashboard and lives at /terminal.html — the same URL
// the old static page had. Keeping it a real file means bookmarks and
// home-screen shortcuts survive the migration, the Go server needs no
// SPA-fallback route, and xterm.js only ships in the bundle that uses it.
const API_PROXY = {
  "/api": { target: "http://127.0.0.1:8585", changeOrigin: true },
  "/ws": { target: "ws://127.0.0.1:8585", ws: true },
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
  build: {
    rollupOptions: {
      input: {
        main: path.resolve(import.meta.dirname, "index.html"),
        terminal: path.resolve(import.meta.dirname, "terminal.html"),
      },
    },
  },
  // Both dev and preview talk to the real Go API, so the dashboard has live
  // data either way. In production the API serves these files itself and every
  // request is same-origin, so no proxy exists there.
  server: { proxy: API_PROXY },
  preview: { proxy: API_PROXY },
})
