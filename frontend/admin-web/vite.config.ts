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
    // 后台只代理 /api/v1/admin/*，服务间接口不对浏览器开放。
    proxy: {
      '/api/v1/admin': {
        target: process.env.DEV_API_PROXY_TARGET || process.env.VITE_API_BASE || 'http://localhost:8081',
        changeOrigin: true,
      },
    },
  },
});
