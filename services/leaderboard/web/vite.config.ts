import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build output goes into the Go service's embed target
// (internal/web/dist), which is //go:embed-ed into the binary and served by the
// Go HTTP server. emptyOutDir:true is required because outDir is outside the
// Vite project root.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    // Dev convenience: proxy the SSE endpoint to the running Go service so
    // `npm run dev` shows live data. Prod serves both from the same origin.
    proxy: {
      "/events": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});
