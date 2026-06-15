import { fileURLToPath, URL } from 'node:url'
import { writeFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

// keepDistPlaceholder：构建后在 outDir 重建 .gitkeep 占位文件。
// 为什么需要：build.emptyOutDir=true 每次构建会清空 ../api/dist，连带删除入库的 .gitkeep；
// 而该占位文件是 api/static.go 的 //go:embed all:dist 的承重物——全新 clone 未构建前端时，
// 没有它 go build ./api/... 会因「no matching files」编译失败。closeBundle 在产物写完后重建它，
// 使 .gitkeep 始终存在，彻底消除「每次 pnpm build 后 git 显示 .gitkeep 被删」的反复问题。
function keepDistPlaceholder(outDir) {
  return {
    name: 'keep-dist-placeholder',
    closeBundle() {
      writeFileSync(fileURLToPath(new URL(`${outDir}/.gitkeep`, import.meta.url)), '')
    },
  }
}

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
    // 构建后重建 ../api/dist/.gitkeep（go:embed 占位，emptyOutDir 会清掉它）
    keepDistPlaceholder('../api/dist'),
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
    rollupOptions: {
      output: {
        // 手动拆包：将 vue 生态与 echarts 单独提取为命名 chunk，
        // 利用浏览器缓存：业务代码迭代时这两个大包不会随之失效（哈希稳定），
        // 同时也让 index chunk 体积更小、首屏解析更快。
        manualChunks: {
          // vue 核心 + 路由 + 状态管理，变动频率极低
          'vendor-vue': ['vue', 'vue-router', 'pinia'],
          // echarts 按需包，已用 use() 裁剪（见 EChart.js），单独缓存
          'vendor-echarts': ['echarts/core', 'echarts/charts', 'echarts/components', 'echarts/renderers'],
        },
      },
    },
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
