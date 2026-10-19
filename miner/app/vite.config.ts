import { defineConfig } from "vite";

// The interface: plain TypeScript, no framework. Builds to dist/ for the Tauri host.
export default defineConfig({
  clearScreen: false,
  server: { port: 1420, strictPort: true },
  build: {
    target: ["es2021", "chrome100", "safari14"],
    minify: false,
    sourcemap: false,
  },
});
