import { card, expect, people, submitSignIn, test, workspace } from './rove.js'

// Run against the second server, the one with `app.consent` (playwright.config.ts).

test('terms are agreed to before anything else, once, and 내 정보 lists the agreements', async ({ page }) => {
	await page.goto(`${workspace}/`)
	await submitSignIn(page, people.member.login)

	await expect(page.getByText('Rove를 쓰려면 아래 문서에 동의해야 합니다.')).toBeVisible()
	const go = page.getByRole('button', { name: '동의하고 계속' })
	await expect(go).toBeDisabled()
	await page.getByRole('checkbox', { name: /이용약관/ }).check()
	await expect(go).toBeDisabled()
	await page.getByRole('checkbox', { name: /개인정보 처리방침/ }).check()
	await go.click()

	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
	await expect(page).toHaveURL(new RegExp(`${workspace}/$`))
	// Agreed once, it is not asked again.
	await page.reload()
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
	await expect(page.getByText('Rove를 쓰려면 아래 문서에 동의해야 합니다.')).toHaveCount(0)

	await page.getByRole('link', { name: new RegExp(`^${people.member.name}`) }).click()
	const agreed = card(page, '동의한 문서').getByRole('listitem')
	await expect(agreed.filter({ hasText: '이용약관' })).toContainText('2026-10-01')
	await expect(agreed.filter({ hasText: '개인정보 처리방침' })).toContainText('2026-10-01')
})
