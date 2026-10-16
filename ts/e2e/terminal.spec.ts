import { createHash, randomBytes } from 'node:crypto'

import { expect, owner, submitSignIn, test } from './rove.js'

// `rove login`, with the test as the terminal (cli/client.go): it makes up a
// key, sends only its hash, and the person signed in at the page approves it.

test('a terminal sign-in is approved at the page, and the terminal then holds a session', async ({ page, request }) => {
	const key = randomBytes(32).toString('base64url')
	const secret = createHash('sha256').update(key).digest('base64url')

	const asked = await request.post('/cli/logins', { data: { secret, client: 'e2e terminal' } })
	expect(asked.status()).toBe(201)
	const started = (await asked.json()) as { id: string; code: string; approve: string; cookie: string }

	// The address it is told to open, as `rove login` prints it.
	await page.goto(started.approve)
	await submitSignIn(page, owner.login, owner.password)
	await expect(page.getByRole('heading', { name: '터미널 로그인' })).toBeVisible()
	await expect(page.getByText(started.code, { exact: true })).toBeVisible()
	await expect(page.getByText('e2e terminal')).toBeVisible()
	await page.getByRole('button', { name: '승인', exact: true }).click()
	await expect(page.getByText('승인했습니다. 터미널로 돌아가세요.')).toBeVisible()

	const polled = await request.post(`/cli/logins/${started.id}/poll`, { data: { secret } })
	expect(await polled.json()).toEqual({ state: 'approved' })

	// The key it kept is a session of the account that approved it.
	const account = await request.get('/account', { headers: { cookie: `${started.cookie}=${key}` } })
	expect(account.status()).toBe(200)
	expect(await account.json()).toMatchObject({ login: owner.login })
})

// `rove login --allow` (D33): what the terminal asks to be narrowed to is on
// the page that approves it, and the session it gets does that much.
test('a terminal that asks for less is shown so, and its session does no more', async ({ page, request }) => {
	const key = randomBytes(32).toString('base64url')
	const secret = createHash('sha256').update(key).digest('base64url')
	const allow = ['/rove.*/Get', '/rove.*/List']

	const asked = await request.post('/cli/logins', { data: { secret, client: 'e2e narrowed terminal', allow } })
	expect(asked.status()).toBe(201)
	const started = (await asked.json()) as { id: string; code: string; approve: string; cookie: string }

	await page.goto(started.approve)
	await submitSignIn(page, owner.login, owner.password)
	await expect(page.getByText('e2e narrowed terminal')).toBeVisible()
	for (const v of allow) await expect(page.getByText(v, { exact: true })).toBeVisible()
	await expect(page.getByText('이 터미널은 이 작업만 할 수 있습니다.')).toBeVisible()
	await page.getByRole('button', { name: '승인', exact: true }).click()
	await expect(page.getByText('승인했습니다. 터미널로 돌아가세요.')).toBeVisible()
	await request.post(`/cli/logins/${started.id}/poll`, { data: { secret } })

	const cookie = `${started.cookie}=${key}`
	const account = await request.get('/account', { headers: { cookie } })
	expect(await account.json()).toMatchObject({ login: owner.login, allow })
	// A write it did not ask for, though the owner may.
	const add = await request.post('/rove.AssetService/Add', {
		headers: { cookie, 'connect-protocol-version': '1' },
		data: { name: '몰래 넣은 자산', kind: 'item' },
	})
	expect(add.status()).toBe(403)
})
