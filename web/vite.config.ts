import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Every script of a build carries this in its name. The hash in a name does
// not change when only what the script imports was renamed, and the panel
// tells browsers to keep these files for good: without this a browser could
// hold on to a script that imports one from a build that is gone.
const build = Date.now().toString(36);

// In development the UI is served by Vite and talks to a panel on :8080.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": { target: "http://localhost:8080", ws: true },
    },
  },
  build: {
    target: "es2022",
    rolldownOptions: {
      output: {
        entryFileNames: `assets/[name]-[hash]-${build}.js`,
        chunkFileNames: `assets/[name]-[hash]-${build}.js`,
      },
    },
  },
});
