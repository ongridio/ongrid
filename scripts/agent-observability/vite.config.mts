import { mergeConfig, type Plugin } from '../../web/node_modules/vite/dist/node/index.js';
import base from '../../web/vite.config';

// Preview-only identity for the loopback server in preview.go.
const entry: Plugin = {
  name: 'local-agent-preview-entry',
  configureServer(server) {
    server.middlewares.use('/agent-preview', (_request, response) => {
      response.setHeader('Content-Type', 'text/html; charset=utf-8');
      response.end(`<!doctype html><title>Agent preview</title><script>
        localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'local-preview-only', role: 'viewer', email: 'preview@example.test' }, version: 0 }));
        localStorage.setItem('sidebar.section.observability', 'true');
        location.replace('/agent-observability');
      </script>`);
    });
  },
};

export default mergeConfig(base, {
  plugins: [entry],
  server: { host: '127.0.0.1', port: 15173, strictPort: true, proxy: { '/api': { target: 'http://127.0.0.1:18590', changeOrigin: true } } },
});
