import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  build: { assetsDir: "console-assets" },
  server: {
    proxy: { "/v1": "http://localhost:8088", "/auth": "http://localhost:8088" },
  },
});
