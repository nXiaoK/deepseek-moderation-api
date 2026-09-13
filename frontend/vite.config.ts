import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
export default defineConfig({
  plugins: [vue()],
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("/node_modules/") && id.includes("/zrender/"))
            return "chart-renderer";
          if (id.includes("/node_modules/") && id.includes("/echarts/"))
            return "charts";
        },
      },
    },
  },
  server: {
    proxy: {
      "/admin": "http://127.0.0.1:8090",
      "/v1": "http://127.0.0.1:8090",
    },
  },
});
