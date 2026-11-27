import { fileURLToPath, URL } from 'node:url';
import { configDefaults, defineConfig } from 'vitest/config';
import type { Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

function landingAtRoot(): Plugin {
	const rewrite = (req: { url?: string }) => {
		if (req.url === '/') req.url = '/landing.html';
	};

	return {
		name: 'landing-at-root',
		configureServer(server) {
			server.middlewares.use((req, _res, next) => {
				rewrite(req);
				next();
			});
		},
		configurePreviewServer(server) {
			server.middlewares.use((req, _res, next) => {
				rewrite(req);
				next();
			});
		},
	};
}

export default defineConfig({
	plugins: [landingAtRoot(), react(), tailwindcss()],
	resolve: {
		alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
	},
	build: {
		rollupOptions: {
			input: {
				main: fileURLToPath(new URL('./index.html', import.meta.url)),
				landing: fileURLToPath(new URL('./landing.html', import.meta.url)),
			},
		},
	},
	server: {
		proxy: { '/api': { target: 'http://localhost:8080' } },
	},
	test: {
		environment: 'jsdom',
		environmentOptions: { jsdom: { url: 'http://localhost:5173' } },
		setupFiles: ['./src/test/setup.ts'],
		exclude: [...configDefaults.exclude, 'tests/e2e/**'],
		css: true,
	},
});
