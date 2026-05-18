import path from 'node:path';
import { writeFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// Continuum mounts each plugin under /api/v1/plugins/{installationId}/, where
// installationId is assigned at install time. Using a relative base ("./")
// makes asset URLs resolve against the current page's path, so the SPA works
// regardless of installation ID.
export default defineConfig({
  base: './',
  plugins: [
    react(),
    tailwindcss(),
    {
      name: 'preserve-dist-gitkeep',
      closeBundle() {
        writeFileSync(path.resolve(__dirname, 'dist/.gitkeep'), '\n');
      },
    },
  ],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  build: { outDir: 'dist', emptyOutDir: true },
});
