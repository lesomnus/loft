import { expect, owner, people, signIn, test } from './loft.js'

// The people screen (D87): who is a member, and how they sign in, is roster's
// -- added and taken away there, their passwords set there -- and the screen
// keeps the person records and what a member may do here. It says so to the
// people who run the workspace, and a member's own page says where their way
// in is kept.

test('the people screen keeps person records, and leaves members and how they sign in to roster', async ({ page }) => {
	await signIn(page, owner.login, owner.password)

	await page.getByRole('navigation').getByRole('link', { name: /사람/ }).click()
	await expect(page.getByRole('note', { name: '구성원 관리' })).toContainText('구성원과 로그인은 roster에서 관리합니다')

	// A member: what they may do here, and nothing of how they sign in.
	await page.getByRole('listitem').filter({ hasText: people.member.name }).first().click()
	const dialog = page.getByRole('dialog')
	await expect(dialog.getByRole('button', { name: '역할 변경' })).toBeVisible()
	await expect(dialog.getByRole('button', { name: '개인정보 삭제' })).toBeVisible()
	await expect(dialog.getByRole('button', { name: /비밀번호|로그인/ })).toHaveCount(0)

	await page.goto('/me')
	await expect(page.getByRole('heading', { name: '내 정보' })).toBeVisible()
	await expect(page.getByText('로그인 방법은 roster에서 관리합니다')).toBeVisible()
	await expect(page.getByRole('button', { name: '비밀번호 변경' })).toHaveCount(0)
})
