import { createHash } from 'node:crypto'
import { readdirSync, readFileSync } from 'node:fs'

import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

/**
 * `npm run dev` serves the page on 5173 and the app answers on 8080. Rather
 * than two origins -- and a cookie that has to cross between them -- the dev
 * server passes everything that is not the page through to the app, so the
 * page sees one origin exactly as it does when the app serves `dist/` itself.
 */
const app = process.env['LOFT_ADDR'] ?? 'http://localhost:8080'

/**
 * The service worker (`sw.js`), written into the build with what it keeps:
 * every file the build has, so the app opens with no network the first time
 * it is needed. Its version is what is in those files, so a build that
 * changed any of them is a new worker.
 */
function serviceWorker(): Plugin {
	const at = (p: string) => new URL(p, import.meta.url)
	return {
		name: 'loft-service-worker',
		apply: 'build',
		generateBundle(_, bundle) {
			const own = readdirSync(at('./public/')).map((f) => `/${f}`)
			const built = Object.keys(bundle)
				.filter((f) => f.startsWith('static/'))
				.map((f) => `/${f}`)
			const files = [...own, ...built].sort()
			const h = createHash('sha256').update(files.join('\n'))
			for (const f of own) h.update(readFileSync(at(`./public${f}`)))
			const version = h.digest('hex').slice(0, 16)
			this.emitFile({
				type: 'asset',
				fileName: 'sw.js',
				source: `const VERSION = ${JSON.stringify(version)}\nconst FILES = ${JSON.stringify(files)}\n\n${readFileSync(at('./sw.js'), 'utf8')}`,
			})
		},
	}
}

export default defineConfig({
	plugins: [react(), serviceWorker()],
	server: {
		proxy: {
			'^/(loft|payday)\\.[A-Za-z]+Service/': { target: app, changeOrigin: false },
			'/session': { target: app, changeOrigin: false },
			'/account': { target: app, changeOrigin: false },
			'/sso': { target: app, changeOrigin: false },
			'/handoff': { target: app, changeOrigin: false },
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
