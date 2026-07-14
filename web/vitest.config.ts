import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config';

// vitest piggy-backs on vite.config.ts so the @/ alias and React plugin
// stay in sync with `npm run dev` / `npm run build`. Test-only knobs go
// in the `test` block below.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      globals: true,
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      css: false,
    },
  }),
);
