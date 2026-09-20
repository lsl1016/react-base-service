import { fileURLToPath, URL } from "node:url";
import { defineConfig, loadEnv } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";

// MCP 网关管理台（源码内嵌本仓，构建产物 web/mcp-admin/dist 经 go:embed 挂在
// /react/mcp-admin，接口走 /api/manage/* 兼容层 controllers/http/mcpadmin）。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const proxyTarget = env.VITE_DEV_PROXY_TARGET || "http://127.0.0.1:8080";
  return {
    plugins: [vue(), tailwindcss()],
    resolve: {
      alias: {
        "@": fileURLToPath(new URL("./src", import.meta.url)),
      },
    },
    server: {
      host: true,
      port: 5173,
      proxy: {
        // 开发期代理到主服务（go run main.go，默认 8080；
        // 主服务需将 /api/manage 挂根路径，见 router/http.go）
        "/api": {
          target: proxyTarget,
          changeOrigin: true,
        },
      },
    },
  };
});
