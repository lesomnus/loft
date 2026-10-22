import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'

import type { Request } from '@playwright/test'
import pg from 'pg'

import { escape, expect, owner, people, signIn, test, unique, workspace } from './rove.js'

// A workspace on a domain of its own (rove#155): signed in to at the service's
// address and handed there, serving that workspace and nothing else, its
// labels printed with it and opening on it, and signed out of there -- which
// signs out of the service's too.
//
// The domain is the test server under another name, `<alias>.e2e.test`, which
// this project's browser resolves to it (playwright.config.ts). What only a
// workspace's DNS could say of it -- that it is verified -- is said to the
// server's database instead, as CI's deployment checks say it.

const port = Number(process.env.E2E_PORT ?? 18510)
const service = `http://127.0.0.1:${port}`
const printed = /[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}/

/** A domain made a workspace's active one, as its TXT record and CNAME would. */
async function verified(host: string): Promise<void> {
	// A time the database wrote, in the form it writes them.
	const sql = "UPDATE tenantdomain SET state = 'active', verified_at = date_updated WHERE host = $1"
	const at = process.env.E2E_POSTGRES
	if (at !== undefined && at !== '') {
		const u = new URL(at)
		u.pathname = `/e2e_rove_${port}`
		const db = new pg.Client({ connectionString: u.toString() })
		await db.connect()
		try {
			expect((await db.query(sql, [host])).rowCount).toBe(1)
		} finally {
			await db.end()
		}
		return
	}
	const db = new DatabaseSync(fileURLToPath(new URL(`../test-results/rove-${port}/rove.db`, import.meta.url)))
	try {
		db.exec('PRAGMA busy_timeout = 10000')
		expect(db.prepare(sql.replace('$1', '?')).run(host).changes).toBe(1)
	} finally {
		db.close()
	}
}

test('a workspace on its own domain is signed in to at the service, serves itself alone, and signs out of both', async ({ page, as }) => {
	await signIn(page, people.domains.login)

	// A workspace of the person's own beside the demo's, and a domain of it.
	const made = await page.request.post('/account/workspaces', { data: { name: unique('e2e 도메인') } })
	expect(made.status()).toBe(201)
	const ws = (await made.json()) as { alias: string; name: string }
	const host = `${ws.alias}.e2e.test`
	const added = await page.request.post('/rove.TenantDomainService/Add', {
		headers: { 'connect-protocol-version': '1', 'rove-workspace': ws.alias },
		data: { host },
	})
	expect(added.status(), await added.text()).toBe(200)
	await verified(host)
	const domain = `http://${host}:${port}`

	// A label of the demo's, printed by its owner with the service's address:
	// its workspace has no domain of its own.
	const demoOwner = await as(owner.login, owner.password)
	await demoOwner.goto(`${workspace}/labels/print?count=1`)
	const theirs = (await demoOwner.getByText(printed).innerText()).match(printed)?.[0]
	expect(theirs).toBeDefined()

	// Opened at the service, the workspace is on its domain: the switcher
	// goes there itself, and nobody signs in there -- the service's sign-in
	// hands it a session of its own, landing in the workspace.
	await page.goto(`${service}/@acme/`)
	const went: string[] = []
	const going = (r: Request): void => {
		if (r.isNavigationRequest() && r.frame() === page.mainFrame()) went.push(r.url())
	}
	page.on('request', going)
	await page.locator('summary').click()
	await page.getByRole('button', { name: new RegExp(`^${escape(ws.name)}`) }).click()
	await expect(page).toHaveURL(new RegExp(`^${escape(domain)}/@${escape(ws.alias)}/`))
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
	page.off('request', going)
	expect(went[0], 'the switcher goes to the domain, rather than to the service to be sent there').toBe(`${domain}/@${ws.alias}/`)
	// And the service sends a page of it there, whoever asked for it.
	await page.goto(`${service}/@${ws.alias}/assets?q=e2e`)
	await expect(page).toHaveURL(`${domain}/@${ws.alias}/assets?q=e2e`)

	// That workspace and no other: the switcher has it alone and offers
	// nothing of the account's beside it, and a call naming the demo's is
	// refused there.
	const switcher = page.locator('summary')
	await expect(switcher).toHaveText(ws.name)
	await switcher.click()
	await expect(page.locator('.switcher li')).toHaveCount(1)
	await expect(page.getByRole('button', { name: '+ 새 워크스페이스' })).toHaveCount(0)
	await expect(page.getByRole('link', { name: /받은 초대/ })).toHaveCount(0)
	// The account's others are the service's to open, and it asks which,
	// rather than open again the one this browser opened last.
	await page.getByRole('link', { name: '다른 워크스페이스 ↗' }).click()
	await expect(page).toHaveURL(`${service}/?workspaces`)
	await expect(page.getByRole('button', { name: /@acme/ })).toBeVisible()
	await expect(page.getByRole('button', { name: new RegExp(`@${escape(ws.alias)}`) })).toBeVisible()
	// And a label of another of them, scanned at the service's address, opens
	// in its own workspace there -- not in the one this browser opened last,
	// whose domain serves its own labels alone.
	await page.goto(`${service}/l/${theirs!.replace('-', '')}`)
	await expect(page.getByRole('heading', { name: '아직 아무 자산에도 붙지 않은 라벨' })).toBeVisible()
	await expect(page).toHaveURL(new RegExp(`^${escape(service)}/@acme/scan/`))
	await page.goto(`${domain}/@${ws.alias}/`)
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
	const demo = await page.evaluate(async () => {
		const res = await fetch('/rove.PartyService/Me', {
			method: 'POST',
			headers: { 'content-type': 'application/json', 'connect-protocol-version': '1', 'rove-workspace': 'acme' },
			body: '{}',
		})
		return res.status
	})
	expect(demo, "the demo's workspace, asked for on this one's domain").toBe(403)

	// A label printed there is printed with the domain, and opens on it.
	await page.goto(`${domain}/@${ws.alias}/labels/print?count=1`)
	await expect(page.getByText(`라벨 1장 · ${host}`)).toBeVisible()
	const code = (await page.getByText(printed).innerText()).match(printed)?.[0]
	expect(code).toBeDefined()
	await page.goto(`${domain}/l/${code!.replace('-', '')}`)
	await expect(page.getByRole('heading', { name: '아직 아무 자산에도 붙지 않은 라벨' })).toBeVisible()
	await expect(page).toHaveURL(new RegExp(`^${escape(domain)}/`))

	// Signing out there is signing out of the service's too: the browser
	// lands at its sign-in, and the domain is handed back to it to sign in
	// again.
	await page.getByRole('button', { name: '로그아웃', exact: true }).click()
	await expect(page).toHaveURL(`${service}/`)
	await expect(page.getByLabel('아이디 또는 이메일')).toBeVisible()
	await page.goto(`${domain}/`)
	await expect(page).toHaveURL(new RegExp(`^${escape(service)}/\\?next=`))
	await expect(page.getByLabel('아이디 또는 이메일')).toBeVisible()
})
