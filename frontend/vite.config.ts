import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Scorched frontend — Vite dev/build config.
// REACT_APP_* env vars from .env.example were written for a Create React
// App-style setup (process.env.REACT_APP_*). Vite uses import.meta.env and
// requires a VITE_ prefix instead — see the note in App.tsx and AUDIT.md.
// This config re-exposes the existing REACT_APP_* names as VITE_ equivalents
// so .env.example doesn't need renaming, but if you add new env vars, prefix
// them VITE_ directly.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    host: '0.0.0.0',
  },
  preview: {
    port: 3000,
    host: '0.0.0.0',
  },
  envPrefix: ['VITE_', 'REACT_APP_'],
});
