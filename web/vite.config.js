import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

// Vite 构建配置：
// - 产物输出到 ../api/dist，供 Go embed 嵌入单一二进制（计划阶段 9）。
// - dev 环境把 /api 反向代理到本地 Gin 后台，前后端分离调试。
// - 自动按需引入 Element Plus 组件与 API，减小手写 import 样板（DRY）。
export default defineConfig({
  plugins: [
    vue(),
    // 自动导入 Vue / vue-router / pinia 的常用 API 与 Element Plus 组件 API
    AutoImport({
      imports: ['vue', 'vue-router', 'pinia'],
      resolvers: [ElementPlusResolver()],
      dts: 'src/auto-imports.d.ts',
    }),
    // 自动按需注册 Element Plus 组件
    Components({
      resolvers: [ElementPlusResolver()],
      dts: 'src/components.d.ts',
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    // 产物输出到 Go 后端的 embed 目录
    outDir: '../api/dist',
    emptyOutDir: true,
    // 单二进制场景下不生成 sourcemap，控制体积
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      // dev 期把 API 与 SSE 请求代理到本地 Gin 后台（默认 8080，可按需调整）
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
