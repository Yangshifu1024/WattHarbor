import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  plugins: [react()],
  base: '/admin/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          antd: ['antd', '@ant-design/icons'],
          react: ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    watch: process.env.DEV_USE_POLLING === 'true'
      ? { usePolling: true, interval: 500 }
      : undefined,
    // 后台只调 /api/v1/admin/*。原先还代理 /api/v1/internal，但源码里没有任何
    // 一处调用它——留着它等于把服务间前缀也暴露给浏览器，没有理由。
    proxy: {
      '/api/v1/admin': {
        target: process.env.DEV_API_PROXY_TARGET || process.env.VITE_API_BASE || 'http://localhost:8081',
        changeOrigin: true,
      },
    },
  },
});
