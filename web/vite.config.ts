import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/v1': {
        target: process.env.VITE_DEV_PROXY_TARGET ?? 'http://127.0.0.1:11000',
        changeOrigin: true,
      },
    },
  },
})
