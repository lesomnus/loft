import { expect, people, submitSignIn, test, workspace } from './rove.js'

test('a password signs in to the workspace, a wrong one does not, and signing out ends it', async ({ page }) => {
	const p = people.signIn
	await page.goto('/')

	await submitSignIn(page, p.login, 'not-the-password')
	await expect(page.getByText('아이디 또는 비밀번호가 맞지 않습니다.')).toBeVisible()

	await submitSignIn(page, p.login)
	// An account in one workspace is taken into it.
	await expect(page).toHaveURL(new RegExp(`${workspace}/$`))
	await expect(page.getByRole('link', { name: new RegExp(`^${p.name}`) })).toBeVisible()

	await page.getByRole('button', { name: '로그아웃', exact: true }).click()
	await expect(page.getByRole('button', { name: '로그인', exact: true })).toBeVisible()
	// And it stays ended: the cookie names a session that is gone.
	await page.reload()
	await expect(page.getByRole('button', { name: '로그인', exact: true })).toBeVisible()
})
