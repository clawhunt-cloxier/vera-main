import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const api = process.env.VERA_API ?? 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { '/api': api, '/auth': api, '/dev-oidc': api, '/healthz': api, '/readyz': api },
  },
  build: { target: 'es2022', sourcemap: false, chunkSizeWarningLimit: 1200 },
  esbuild: { logOverride: { 'this-is-undefined-in-esm': 'silent' } },
});
