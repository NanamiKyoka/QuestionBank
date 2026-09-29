import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 后端地址：开发时通过 Vite 代理转发，避免 CORS。
const backend = process.env.QM_BACKEND ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // 与后端 main.go 的挂载点保持一致：/api、/static、/uploads。
      '/api': { target: backend, changeOrigin: true },
      '/static': { target: backend, changeOrigin: true },
      '/uploads': { target: backend, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    // 生产由后端或 nginx 托管静态文件，不需要 sourcemap。
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
})
