import { defineConfig } from 'vitest/config';
import type { Plugin } from 'vite';
import react from '@vitejs/plugin-react-swc';
import { fileURLToPath } from 'node:url';

// Resolved at runtime by the host import map, so the plugin shares the host React singleton.
const HOST_PROVIDED = ['react', 'react-dom', 'react/jsx-runtime'];

const srcDir = (dir: string) =>
  fileURLToPath(new URL(`./src/${dir}`, import.meta.url));

// A bundled CommonJS dep that require()s a host-provided module compiles to a stub
// that throws on load, and the host only logs plugin load errors to the console.
const failOnHostRequire = (): Plugin => ({
  name: 'fail-on-host-require',
  apply: 'build',
  renderChunk(code, chunk) {
    for (const id of HOST_PROVIDED) {
      if (code.includes(`__require("${id}")`)) {
        this.error(`${chunk.fileName} calls require("${id}"); alias that CommonJS dependency to ESM`);
      }
    }
    return null;
  },
});

export default defineConfig(({ command }) => ({
  plugins: [react(), failOnHostRequire()],
  resolve: {
    alias: [
      { find: 'api', replacement: srcDir('api') },
      { find: 'components', replacement: srcDir('components') },
      { find: 'hooks', replacement: srcDir('hooks') },
      { find: 'types', replacement: srcDir('types') },
      { find: 'utils', replacement: srcDir('utils') },
    ],
  },
  // Library mode leaves process.env untouched, but MUI and React Query read NODE_ENV.
  define:
    command === 'build'
      ? { 'process.env.NODE_ENV': JSON.stringify('production') }
      : undefined,
  build: {
    lib: {
      entry: 'src/main.tsx',
      formats: ['es'],
      fileName: () => 'main.js',
    },
    rollupOptions: {
      external: HOST_PROVIDED,
    },
  },
  test: {
    environment: 'jsdom',
  },
}));
