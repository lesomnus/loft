import { card, choose, expect, field, people, said, signIn, test, unique, weekdayAt, workspace } from './rove.js'

// A hand-over started where the thing is: the asset's own page, which puts the
// asset in the dialog, and which afterwards says who has it.

test('a phone is lent from its own page, and the page then says who has it', async ({ page }) => {
	const memo = unique('e2e 현장 지원')
	const member = people.lent
	await signIn(page, people.custody.login)

	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill('PH-003')
	await page.getByRole('link', { name: '업무폰 3', exact: true }).click()
	const head = page.getByRole('heading', { level: 1 })
	await expect(head).toContainText('PH-003')
	const asset = page.url()
	await expect(card(page, '정보')).toContainText(/사용자\s*-/)

	await page.getByRole('button', { name: '지급·대여', exact: true }).click()
	const give = page.getByRole('dialog')
	// The page's asset is what is handed over, and nothing else has to be found.
	await expect(give.getByRole('listitem').filter({ hasText: 'PH-003' })).toContainText('업무폰 3')
	await choose(field(give, '받는 사람'), member.name)
	await choose(field(give, '구분'), '대여')
	await field(give, '반납 기한').fill(weekdayAt(5, 18))
	await field(give, '메모').fill(memo)
	await give.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')

	await expect(head).toHaveText(`대여: ${member.name}`)
	await expect(card(page, '내용')).toContainText(memo)
	await expect(card(page, '내용')).toContainText(/인수 확인\s*대기/)
	await expect(card(page, '품목').getByRole('listitem')).toContainText('PH-003')

	// And the asset's page, now: who has it, no second hand-over, and the
	// hand-over on its timeline.
	await page.goto(asset)
	await expect(card(page, '정보')).toContainText(new RegExp(`사용자\\s*${member.name}`))
	await expect(page.getByRole('button', { name: '지급·대여', exact: true })).toHaveCount(0)
	const held = card(page, '이력').getByRole('listitem').filter({ hasText: `보유: ${member.name}` })
	await expect(held).toContainText(memo)
	await expect(held).toContainText(people.custody.name)
})
