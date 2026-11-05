import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  /**
   * 相对基址，便于同一份产物适配同源根路径、反代子路径与 wails:// 内嵌资产服务器；绝对 '/' 在子路径下会让资源 404。
   * 与 HashRouter 配合：文档 URL 最多只有一个子路径，相对路径解析无歧义。
   */
  base: './',
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
  },
  server: {
    port: 5273,
    /* 后端跑在 9090，开发时 /api 直接代理过去。 */
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:9090',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      output: {
        /**
         * 按「变更频率」而非包名分组：框架层、UI 库、业务代码各自成 chunk，
         * 改一行业务代码不该让用户重下 React。
         */
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler|react-router|react-router-dom)[\\/]/.test(id)) {
            return 'vendor-react'
          }
          if (/[\\/]node_modules[\\/](@radix-ui|radix-ui|cmdk|lucide-react|sonner)[\\/]/.test(id)) {
            return 'vendor-ui'
          }
          if (id.includes('@tanstack')) return 'vendor-query'
          return 'vendor-misc'
        },
      },
    },
  },
})
