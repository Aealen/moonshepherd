import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { mockPlugin } from './mock-api.mjs'

// 开发模式：/api 与 /agent 由 vite proxy 转发到本地面板（make dev）。
// HERD_MOCK=1 时启用 mock 数据插件(拦截 /api/v1,免登录带全量仿真数据)——
// 用于纯前端审美走查,不依赖真实面板;npm run build 不受影响。
const useMock = process.env.HERD_MOCK === '1'

export default defineConfig({
  plugins: [vue(), ...(useMock ? [mockPlugin()] : [])],
  server: {
    port: 5173,
    proxy: {
      ...(useMock ? {} : { '/api': { target: 'http://127.0.0.1:8090', changeOrigin: true } }),
    },
  },
  build: {
    outDir: 'dist',
    chunkSizeWarningLimit: 1500,
  },
})
