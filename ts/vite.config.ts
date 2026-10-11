import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

/**
 * `npm run dev` serves the page on 5173 and the app answers on 8080. Rather
 * than two origins -- and a cookie that has to cross between them -- the dev
 * server passes everything that is not the page through to the app, so the
 * page sees one origin exactly as it does when the app serves `dist/` itself.
 */
const app = process.env['ROVE_ADDR'] ?? 'http://localhost:8080'

export default defineConfig({
	plugins: [react()],
	server: {
		proxy: {
			'^/(rove|payday)\\.[A-Za-z]+Service/': { target: app, changeOrigin: false },
			'/session': { target: app, changeOrigin: false },
			'/account': { target: app, changeOrigin: false },
			'/files/': { target: app, changeOrigin: false },
			'/l/': { target: app, changeOrigin: false },
			'/healthz': { target: app, changeOrigin: false },
		},
	},
	build: {
		chunkSizeWarningLimit: 2000,
		// Not `assets`, which is a page here: `/assets/<id>` is an asset.
		assetsDir: 'static',
	},
})
