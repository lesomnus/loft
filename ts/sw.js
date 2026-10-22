/**
 * The service worker: the app itself, kept for when there is no network
 * (decided 2026-10-12).
 *
 * It keeps the page and its files and **nothing a page reads**. A read is
 * somebody's in some workspace, and a worker sees neither -- the session is a
 * cookie it cannot read and the workspace a header -- so what a page read is
 * the page's to keep, for whom it was read (`src/offline.ts`). Every request
 * that is not the app's own files goes past this untouched.
 *
 * `vite.config.ts` writes `VERSION` and `FILES` above this when it builds:
 * every file of that build, so that the app opens offline the first time it
 * is needed and not only after each part of it was clicked through. A build
 * with other files is another worker, which is how a browser hears of a new
 * version, and its files replace the last one's.
 */

/* global VERSION, FILES */

const SHELL = `rove-${VERSION}`

self.addEventListener('install', (e) => {
	e.waitUntil(
		caches
			.open(SHELL)
			.then((c) => c.addAll(['/', ...FILES]))
			.then(() => self.skipWaiting()),
	)
})

self.addEventListener('activate', (e) => {
	e.waitUntil(
		caches
			.keys()
			.then((ks) => Promise.all(ks.filter((k) => k.startsWith('rove-') && k !== SHELL).map((k) => caches.delete(k))))
			.then(() => self.clients.claim()),
	)
})

self.addEventListener('fetch', (e) => {
	const req = e.request
	if (req.method !== 'GET') return
	const url = new URL(req.url)
	if (url.origin !== self.location.origin) return

	// A page is the network's, and the last one it gave when there is none:
	// every page is the same page, which routes on the address itself.
	if (req.mode === 'navigate') {
		if (url.pathname.startsWith('/l/')) {
			e.respondWith(label(req, url))
			return
		}
		if (!page(url.pathname)) return
		e.respondWith(navigate(req))
		return
	}
	// The app's own files, named for what is in them, so a copy is never stale.
	if (url.pathname.startsWith('/static/') || FILES.includes(url.pathname)) {
		e.respondWith(file(req))
	}
})

/** Whether an address is the app's, rather than something the server answers itself. */
function page(path) {
	return !/^\/(sso|session|account|files|l|healthz|handoff)(\/|$)/.test(path)
}

async function navigate(req) {
	try {
		const res = await fetch(req)
		// The app, and not a redirect, an error or a page of the server's own.
		if (res.ok && !res.redirected && (res.headers.get('content-type') ?? '').startsWith('text/html')) {
			const c = await caches.open(SHELL)
			await c.put('/', res.clone())
		}
		return res
	} catch (err) {
		const kept = await (await caches.open(SHELL)).match('/')
		if (kept !== undefined) return kept
		throw err
	}
}

/**
 * A printed label, which the server sends on to the page that resolves it
 * (`GET /l/{code}`); with no network the worker sends it on itself, and the
 * page resolves it from what it last saw.
 */
async function label(req, url) {
	try {
		return await fetch(req)
	} catch {
		return Response.redirect(`/scan/${url.pathname.slice('/l/'.length)}`, 302)
	}
}

async function file(req) {
	const c = await caches.open(SHELL)
	const kept = await c.match(req)
	if (kept !== undefined) return kept
	const res = await fetch(req)
	if (res.ok) await c.put(req, res.clone())
	return res
}
