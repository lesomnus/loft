import { choose, expect, field, owner, people, said, signIn, test, unique } from './loft.js'

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

// A member whose first sign-in found none of the records they could be, or
// the wrong one, is linked to theirs by hand (D88): from the record they
// were, which is nobody's login from then; and let go of it again, when they
// are no record's until one is linked to them.
test('a member is linked by hand to the person record they are, and let go of it', async ({ page }) => {
	const name = unique('e2e 사람')
	const login = `@${people.linked.login}`
	await signIn(page, owner.login, owner.password)
	await page.getByRole('navigation').getByRole('link', { name: /사람/ }).click()

	await page.getByRole('button', { name: '+ 사람' }).click()
	await field(page.getByRole('dialog'), '이름').fill(name)
	await page.getByRole('dialog').getByRole('button', { name: '저장' }).click()
	await said(page, '추가했습니다.')
	const mine = page.getByRole('listitem').filter({ hasText: name })
	// The record they are, in their team's card: they are of 영업팀.
	const team = page.locator('section').filter({ has: page.getByRole('heading', { name: /^영업팀/ }) })
	const was = team.getByRole('listitem').filter({ hasText: people.linked.name })

	await mine.click()
	await page.getByRole('dialog').getByRole('button', { name: '구성원과 잇기' }).click()
	const link = page.getByRole('dialog')
	await choose(field(link, '구성원'), login)
	await expect(link.getByText(`지금은 ${people.linked.name}의 기록과 이어져 있습니다.`, { exact: false })).toBeVisible()
	await link.getByRole('button', { name: '잇기' }).click()
	await said(page, '이었습니다.')
	await expect(mine).toContainText('구성원')
	await expect(was).toContainText('로그인 없음')

	await mine.click()
	await page.getByRole('dialog').getByRole('button', { name: '구성원 연결 끊기' }).click()
	// Asked over the record's own dialog, which holds it.
	const sure = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: '확인' }) }).last()
	await sure.getByRole('button', { name: '구성원 연결 끊기' }).click()
	await said(page, '끊었습니다.')
	await expect(mine).toContainText('로그인 없음')
	const recordless = page.locator('section').filter({ has: page.getByRole('heading', { name: /^사람 기록이 없는 구성원/ }) })
	await expect(recordless).toContainText(login)

	// Theirs again, for whoever comes next.
	await was.click()
	await page.getByRole('dialog').getByRole('button', { name: '구성원과 잇기' }).click()
	await choose(field(page.getByRole('dialog'), '구성원'), login)
	await page.getByRole('dialog').getByRole('button', { name: '잇기' }).click()
	await said(page, '이었습니다.')
	await expect(was).toContainText('구성원')
	await expect(recordless).toHaveCount(0)
})
