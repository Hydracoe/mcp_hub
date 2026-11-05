import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  /**
   * 相对基址，让同一份产物在三种挂载点下都能用：
   *   /            后端同源部署（默认）
   *   /admin/      反代到子路径
   *   wails://…    桌面端的内嵌资产服务器
   * 绝对基址（默认的 '/'）只在"部署在根路径"时成立，子路径下资源会 404。
   * 与 HashRouter 配合：文档 URL 最多只有一个子路径，相对路径解析没有歧义。
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
         * 路由级懒加载把「页面」拆开了，但被所有页面共享的依赖仍会各自
         * 抄一份或全部挤进入口。这里按「变更频率」而不是按包名分组：
         * 框架层半年不动，UI 库跟着依赖升级走，业务代码天天改。
         *
         * 分组结果直接决定缓存命中 —— 改一行业务代码不该让用户重下 300KB
         * 的 React，这也是把 vendor 从主 chunk 里拿出去的主要动机。
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
