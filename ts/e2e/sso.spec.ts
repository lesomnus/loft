import type { APIRequestContext } from '@playwright/test'

import { demoPassword, expect, people, test, workspace } from './loft.js'

// Signing in through roster's issuer (auth.sso, server/sso), against the
// test's own (e2e/issuer.sh): the button sends the browser there and it comes
// back as the account the issuer names, and signing out goes back through it.
//
// Run against the third server, the one with `auth.sso` (playwright.config.ts).

test('a person signs in through the issuer as the account it names, and signs out through it', async ({ page, request, baseURL }) => {
	await vouchFor(request, people.sso.login)
	const button = page.getByRole('button', { name: 'Google 계정으로 로그인' })

	await page.goto(`${workspace}/`)
	// The issuer's button, and a password beside it.
	await expect(page.getByText('또는 아이디로')).toBeVisible()
	// The button's connection goes to the issuer with the browser, for roster's
	// page to send it straight on to Google.
	const asked = page.waitForRequest((r) => r.url().startsWith(`${issuer()}/oauth2/auth?`))
	await button.click()
	expect(new URL((await asked).url()).searchParams.get('connection')).toBe('google')
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
	await expect(page).toHaveURL(new RegExp(`${workspace}/$`))
	await expect(page.getByRole('link', { name: new RegExp(`^${people.sso.name}`) })).toBeVisible()

	// Signing out ends the session here and at the issuer, which sends the
	// browser back to where it came from, signed out -- since it is handed the
	// ID token it issued, which is what an issuer wants before it does.
	const ended = page.waitForRequest((r) => r.url().startsWith(`${issuer()}/oauth2/sessions/logout?`))
	await page.getByRole('button', { name: '로그아웃', exact: true }).click()
	const q = new URL((await ended).url()).searchParams
	expect(q.get('id_token_hint')).toMatch(/^[\w-]+\.[\w-]+\.[\w-]+$/)
	expect(q.get('post_logout_redirect_uri')).toBe(`${baseURL}/`)
	await expect(button).toBeVisible()
	await expect(page).toHaveURL(`${baseURL}/`)
	await page.goto(`${workspace}/`)
	await expect(button).toBeVisible()
})

test('somebody who says no at the issuer comes back signed out, and is told so', async ({ page, request }) => {
	await next(request, { refuse: 'access_denied' })

	await page.goto(`${workspace}/`)
	await page.getByRole('button', { name: 'Google 계정으로 로그인' }).click()
	await expect(page.getByText('로그인을 취소했습니다.')).toBeVisible()
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toHaveCount(0)
})

/**
 * Has the issuer sign the next person in as `login`'s account. It names an
 * account by roster's identifier for it, as roster's does after somebody has
 * signed in at Google; the identifier is read where an account reads its own,
 * signed in with its password for the moment it takes.
 */
async function vouchFor(request: APIRequestContext, login: string): Promise<void> {
	const signedIn = await request.post('/session', { data: { login, password: demoPassword } })
	expect(signedIn.status()).toBe(204)
	const account = (await (await request.get('/account')).json()) as { id: string }
	expect(account.id).not.toBe('')
	await request.delete('/session')
	await next(request, { subject: account.id })
}

/** What the issuer answers the next sign-in with (cmd/oidctest). */
async function next(request: APIRequestContext, v: { subject?: string; refuse?: string }): Promise<void> {
	const told = await request.post(`${issuer()}/e2e/next`, { data: v })
	expect(told.status()).toBe(204)
}

/** Where the test's issuer is, which the project says. */
function issuer(): string {
	return test.info().project.metadata.issuer as string
}
