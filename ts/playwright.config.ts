import { defineConfig, devices } from '@playwright/test'

// The browser tests (plan.md M3): the main flows, driven through the built page
// as people do, against a rove of their own that `e2e/serve.sh` builds from
// this checkout and starts with `init --demo` -- on SQLite, or with
// E2E_POSTGRES on databases of its own there, made fresh every run; and with
// E2E_OPENSEARCH searching on that cluster, on indexes of its own.
// `npx playwright test`, on a desk as in CI; README `## 테스트` has the rest.
//
// Three of them, since one asks for agreement to terms (`app.consent`) and the
// others must not -- every sign-in there begins with it -- and one signs people
// in through an issuer (`auth.sso`), the test's own (e2e/issuer.sh), which the
// others have no reason to ask.
const port = Number(process.env.E2E_PORT ?? 18510)
const app = `http://127.0.0.1:${port}`
const consent = `http://127.0.0.1:${port + 10}`
const sso = `http://127.0.0.1:${port + 20}`
const issuer = `http://127.0.0.1:${port + 30}`

// Playwright stops a web server by killing it (SIGKILL, with no
// `gracefulShutdown` set), so a server has nothing to clean up on its way out:
// what it leaves is in test-results/, which the next run empties first.
const server = (at: number, ...args: string[]) => ({
	command: ['sh', 'e2e/serve.sh', String(at), ...args].join(' '),
	url: `http://127.0.0.1:${at}/`,
	// Building rove when E2E_ROVE names none, and `init --demo`, which makes
	// fifty accounts.
	timeout: 180_000,
	reuseExistingServer: false,
})

export default defineConfig({
	testDir: 'e2e',
	forbidOnly: !!process.env.CI,
	// A test that passes on a second try is a test that fails; it is fixed,
	// not retried.
	retries: 0,
	reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
	timeout: 30_000,
	expect: { timeout: 10_000 },
	use: {
		...devices['Desktop Chrome'],
		// The app is written for, and its days are counted in, Korea.
		locale: 'ko-KR',
		timezoneId: 'Asia/Seoul',
		// The page registers a service worker that keeps the app for offline
		// use; a test asks the server every time.
		serviceWorkers: 'block',
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure',
	},
	projects: [
		{ name: 'app', testIgnore: /(consent|sso)\.spec\.ts/, use: { baseURL: app } },
		{ name: 'consent', testMatch: /consent\.spec\.ts/, use: { baseURL: consent } },
		{ name: 'sso', testMatch: /sso\.spec\.ts/, use: { baseURL: sso }, metadata: { issuer } },
	],
	webServer: [
		server(port),
		server(port + 10, 'consent'),
		server(port + 20, 'sso', issuer),
		{
			command: `sh e2e/issuer.sh ${port + 30} ${sso}`,
			url: `${issuer}/.well-known/openid-configuration`,
			timeout: 180_000,
			reuseExistingServer: false,
		},
	],
})
