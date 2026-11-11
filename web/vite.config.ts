import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { TanStackRouterVite } from '@tanstack/router-plugin/vite';

const api = process.env.VERA_API ?? 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [
    TanStackRouterVite({ routesDirectory: './src/routes', generatedRouteTree: './src/routeTree.gen.ts', autoCodeSplitting: false }),
    react(),
  ],
  server: {
    port: 5173,
    proxy: { '/api': api, '/auth': api, '/dev-oidc': api, '/healthz': api, '/readyz': api },
  },
  build: { target: 'es2022', sourcemap: false, chunkSizeWarningLimit: 1200 },
});
