import { fileURLToPath, URL } from 'node:url';
import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
	plugins: [react(), tailwindcss()],
	resolve: {
		alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
	},
	server: {
		proxy: { '/api': { target: 'http://localhost:8080' } },
	},
	test: {
		environment: 'jsdom',
		environmentOptions: { jsdom: { url: 'http://localhost:5173' } },
		setupFiles: ['./src/test/setup.ts'],
		css: true,
	},
});
