import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import federation from '@originjs/vite-plugin-federation'

export default defineConfig({
  plugins: [
    svelte(),
    federation({
      name: 'svelte_app',
      filename: 'remoteEntry.js',
      exposes: {
        './App': './src/App.svelte'
      },
      shared: []
    })
  ],
  build: {
    target: 'esnext'
  },
  server: {
    port: 5001,
    cors: true
  }
})
