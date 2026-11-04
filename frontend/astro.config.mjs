import { defineConfig } from 'astro/config';
import preact from '@astrojs/preact';

// COOP/COEP make the page crossOriginIsolated, which Dusk needs for
// SharedArrayBuffer. The Go server sends the same headers in production.
const isolationHeaders = {
  'Cross-Origin-Opener-Policy': 'same-origin',
  'Cross-Origin-Embedder-Policy': 'credentialless',
};

export default defineConfig({
  integrations: [preact({ compat: true })],
  output: 'static',
  build: { format: 'file' },
  vite: {
    server: {
      headers: isolationHeaders,
      proxy: {
        '/api': 'http://127.0.0.1:3000',
        '/v1': 'http://127.0.0.1:3000',
        '/wisp': { target: 'ws://127.0.0.1:3000', ws: true },
      },
    },
  },
});
