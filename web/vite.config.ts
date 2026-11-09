import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  plugins: [react()],
  base: '/',
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  build: {
    target: 'es2020',
    outDir: 'dist',
    sourcemap: false,
    // modulePreload defaults to preloading every static import in the
    // dependency graph, including chunks routed through dynamic imports
    // because Vite walks the static graph. That made vendor-charts (525 KB)
    // and vendor-xterm (286 KB) ride along on the login page even though
    // only Monitor / DeviceShell actually use them. Strip those two from
    // the preload list — the browser still fetches them when the lazy
    // route loads, just not before login.
    modulePreload: {
      resolveDependencies(_filename, deps) {
        return deps.filter(
          (dep) => !dep.includes('vendor-charts') && !dep.includes('vendor-xterm'),
        );
      },
    },
    rollupOptions: {
      output: {
        // Split *only* the truly heavy lazy-loaded deps so they don't
        // get pulled into the main bundle. Conservative on purpose:
        // aggressive chunking can break init order for libs with
        // top-level side effects (the symptom is a black page on a
        // route that uses something forced into a separate chunk).
        // recharts and xterm are the two that matter for size; leave
        // react / react-router / zustand / lucide / react-markdown in
        // the default bundle so vite figures the order out.
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined;
          if (id.includes('recharts') || id.includes('d3-') || id.includes('victory-')) {
            return 'vendor-charts';
          }
          if (id.includes('xterm')) {
            return 'vendor-xterm';
          }
          return undefined;
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      // The CMDB service runs on its own port; keep the specific prefix
      // before the catch-all so Vite does not send it to the manager proxy.
      '/api/v1/cmdb': {
        target: 'http://localhost:8082',
        changeOrigin: true,
      },
      '/api': {
        target: 'http://localhost:8090',
        changeOrigin: true,
      },
    },
  },
});
