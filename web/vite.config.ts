import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

const developmentBackend = process.env.SCARLET_DEV_API_ORIGIN || 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  publicDir: '../governance/static',
  server: {
    host: '127.0.0.1',
    strictPort: true,
    proxy: {
      '/api': {
        target: developmentBackend,
        changeOrigin: false,
        configure(proxy) {
          proxy.on('proxyReq', (outgoing, incoming) => {
            // Only this explicitly configured, loopback development UI can
            // translate its own Origin to the local Docker application's Origin.
            if (process.env.SCARLET_DEV_API_ORIGIN && incoming.headers.origin === `http://${incoming.headers.host}`) {
              outgoing.setHeader('Origin', new URL(developmentBackend).origin)
            }
          })
        },
      },
    },
  },
})
