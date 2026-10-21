import { expect, test as base, type BrowserContext, type Locator, type Page } from '@playwright/test'

export { expect }

/** The workspace `rove init --demo` makes, which every address is under. */
export const workspace = '/@acme'

/** The password `--demo` gives everybody it makes but the owner. */
export const demoPassword = 'demo1234'

/** Who `e2e/serve.sh` makes the owner. */
export const owner = { login: 'owner', password: 'owner-pass-1234' }

/**
 * The demo's people the tests sign in as, named for what each is there for.
 *
 * Tests run beside each other on one server, so each test that changes
 * something about a person -- what they hold, which workspaces they are in --
 * has people nobody else's test uses.
 */
export const people = {
	signIn: { login: 'jia-lim', name: '임지아' },
	assets: { login: 'jimin-kim', name: '김지민' },
	exports: { login: 'yerin-kim', name: '김예린' },
	labels: { login: 'daeun-kim', name: '김다은' },
	custody: { login: 'minjun-kim', name: '김민준' },
	member: { login: 'haeun-lee', name: '이하은' },
	issued: { login: 'yejun-park', name: '박예준' },
	booker: { login: 'jiwoo-jung', name: '정지우' },
	approver: { login: 'jia-kim', name: '김지아' },
	counts: { login: 'gunwoo-lee', name: '이건우' },
	workspaces: { login: 'yejun-kang', name: '강예준' },
	reporter: { login: 'yujun-park', name: '박유준' },
	stock: { login: 'jia-cho', name: '조지아' },
	lent: { login: 'yerin-choi', name: '최예린' },
	selfService: { login: 'daeun-cho', name: '조다은' },
	camera: { login: 'sunwoo-yoon', name: '윤선우' },
	closer: { login: 'eunwoo-kang', name: '강은우' },
	inviter: { login: 'jiwoo-lee', name: '이지우' },
	invitee: { login: 'daeun-choi', name: '최다은' },
	declines: { login: 'sunwoo-jung', name: '정선우' },
	sso: { login: 'eunwoo-jang', name: '장은우' },
	place: { login: 'hyunwoo-yoon', name: '윤현우' },
} as const

/** A name nothing else in the run has. */
export function unique(prefix: string): string {
	return `${prefix} ${Math.random().toString(36).slice(2, 8)}`
}

/** Fills in the sign-in form and sends it, wherever the page shows it. */
export async function submitSignIn(page: Page, login: string, password = demoPassword): Promise<void> {
	await page.getByLabel('아이디 또는 이메일').fill(login)
	await page.getByLabel('비밀번호').fill(password)
	await page.getByRole('button', { name: '로그인', exact: true }).click()
}

/** Signs in through the form, as a person does, and waits for the workspace. */
export async function signIn(page: Page, login: string, password = demoPassword): Promise<void> {
	await page.goto(`${workspace}/`)
	await submitSignIn(page, login, password)
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
}

/**
 * The tests, with `as`: a browser of its own for somebody else, signed in --
 * a second person at a second desk -- and closed when the test is over.
 *
 * And held to every form field they open naming its control: one that names
 * none says so in the console (ui.tsx, `Field`), and the test it was seen in
 * fails, whichever page it was on.
 *
 * And to the Content Security Policy the pages are shown with: whatever a page
 * does that it does not allow, the browser says in the console, reported only
 * or stopped (`app.csp`, `server/csp`), and the test fails. That is how the
 * policy is known to fit the app before anything is stopped by it.
 */
export const test = base.extend<{ as: (login: string, password?: string) => Promise<Page>; unnamed: string[]; breaches: string[] }>({
	unnamed: [
		async ({ page }, use) => {
			const said: string[] = []
			hear(page, said)
			await use(said)
			expect(said, 'a form field whose label names no control').toEqual([])
		},
		{ auto: true },
	],
	breaches: [
		async ({ page }, use) => {
			const said: string[] = []
			heed(page, said)
			await use(said)
			expect(said, 'what a page did that its Content Security Policy does not allow').toEqual([])
		},
		{ auto: true },
	],
	as: async ({ browser, unnamed, breaches }, use) => {
		const opened: BrowserContext[] = []
		await use(async (login, password = demoPassword) => {
			const context = await browser.newContext()
			opened.push(context)
			const page = await context.newPage()
			hear(page, unnamed)
			heed(page, breaches)
			await signIn(page, login, password)
			return page
		})
		for (const c of opened) await c.close()
	},
})

/** What `page` says in its console about a field that names nothing. */
function hear(page: Page, into: string[]): void {
	page.on('console', (m) => {
		if (m.type() === 'error' && m.text().startsWith('Field:')) into.push(m.text())
	})
}

/**
 * What `page` says in its console about breaking its Content Security Policy:
 * for a page a test opens in a browser of its own, which the fixture does not
 * see, with `breaches` as `into`.
 */
export function heed(page: Page, into: string[]): void {
	page.on('console', (m) => {
		if (m.text().includes('Content Security Policy')) into.push(m.text())
	})
}

/**
 * Picks the option of a `<select>` whose text has `text` in it. The app's
 * options carry more than a name -- a team, a path, a kind -- and are loaded
 * after the page is, so this waits for the option and does not ask for its
 * whole label.
 */
export async function choose(select: Locator, text: string): Promise<void> {
	const option = select.locator('option', { hasText: text }).first()
	await expect(option).toBeAttached()
	const value = await option.getAttribute('value')
	if (value === null) throw new Error(`the option "${text}" has no value`)
	await select.selectOption(value)
}

/**
 * The control of the form field named `name`, or the field itself when it is
 * a group of controls -- an asset picker, a row of boxes -- whose own controls
 * are then found in it by theirs. A field's label says its name and nothing
 * else (ui.tsx, `Field`); a required one has a `*` after it, which `name` may
 * leave off.
 */
export function field(scope: Page | Locator, name: string): Locator {
	return scope.getByLabel(new RegExp(`^${escape(name)}( \\*)?$`))
}

/** `text` as a pattern that matches it and nothing else. */
export function escape(text: string): string {
	return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** The card with this heading. */
export function card(page: Page, title: string): Locator {
	return page.locator('section').filter({ has: page.getByRole('heading', { name: title, exact: true }) })
}

/**
 * Waits for the page to say `text` in a toast, and fails with what it said
 * instead when what it said was an error.
 *
 * A toast stays a few seconds, so one this has seen is marked as seen: the
 * second of two returns waits for its own 반납 처리했습니다, not the first's.
 */
export async function said(page: Page, text: string): Promise<void> {
	const fresh = page.locator('.toasts > .toast:not([data-said])')
	const ok = fresh.filter({ hasText: new RegExp(`^${escape(text)}$`) }).first()
	const bad = fresh.and(page.locator('.bad')).first()
	await expect(ok.or(bad).first()).toBeVisible()
	if (!(await ok.isVisible())) throw new Error(`the page said "${await bad.innerText()}" where "${text}" was expected`)
	// One that went by itself before it was marked cannot be seen again.
	await ok.evaluate((el) => el.setAttribute('data-said', ''), undefined, { timeout: 1000 }).catch(() => {})
}

/**
 * A `datetime-local` value `days` from today, at `hour` o'clock in Korea --
 * where the browser is (`timezoneId`) and where the opening hours are kept --
 * on the first weekday from then.
 */
export function weekdayAt(days: number, hour: number): string {
	const d = new Date(Date.now() + days * 86400_000)
	for (;;) {
		const wd = new Intl.DateTimeFormat('en-US', { timeZone: 'Asia/Seoul', weekday: 'short' }).format(d)
		if (wd !== 'Sat' && wd !== 'Sun') break
		d.setTime(d.getTime() + 86400_000)
	}
	const day = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit' }).format(d)
	return `${day}T${String(hour).padStart(2, '0')}:00`
}
