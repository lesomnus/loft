import { card, choose, expect, field, people, said, signIn, test, unique, weekdayAt, workspace } from './loft.js'

test('a loan is handed over, acknowledged by the member it went to, and returned', async ({ page, as }) => {
	const memo = unique('e2e 출장')
	const member = people.member
	await signIn(page, people.custody.login)

	await page.goto(`${workspace}/custody`)
	await page.getByRole('button', { name: '+ 지급·대여' }).click()
	const give = page.getByRole('dialog')
	await choose(field(give, '받는 사람'), member.name)
	await choose(field(give, '구분'), '대여')
	await field(give, '반납 기한').fill(weekdayAt(7, 18))
	const add = field(give, '자산 추가')
	await add.getByRole('searchbox').fill('NB-052')
	await add.getByRole('button', { name: /NB-052/ }).click()
	await field(give, '메모').fill(memo)
	await give.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(`대여: ${member.name}`)
	const handed = page.url()

	// The member, at a desk of their own, says it arrived.
	const theirs = await as(member.login)
	await theirs.goto(`${workspace}/me`)
	const waiting = card(theirs, '인수 확인이 필요한 것').getByRole('listitem').filter({ hasText: memo })
	await waiting.getByRole('button', { name: '받았습니다' }).click()
	await said(theirs, '확인했습니다.')
	await expect(waiting).toHaveCount(0)

	await page.goto(handed)
	await expect(page.getByRole('button', { name: '인수 확인' })).toHaveCount(0)
	await page.getByRole('button', { name: '반납 받기' }).click()
	await page.getByRole('dialog').getByRole('button', { name: '반납', exact: true }).click()
	await said(page, '반납 처리했습니다.')
	await expect(page.getByText('반납 완료', { exact: true })).toBeVisible()
})

test('an issue of two is returned in part, and stays open until the rest is back', async ({ page }) => {
	await signIn(page, people.custody.login)

	await page.goto(`${workspace}/custody`)
	await page.getByRole('button', { name: '+ 지급·대여' }).click()
	const give = page.getByRole('dialog')
	await choose(field(give, '받는 사람'), people.issued.name)
	await choose(field(give, '구분'), '지급')
	const add = field(give, '자산 추가')
	for (const tag of ['NB-054', 'NB-055']) {
		await add.getByRole('searchbox').fill(tag)
		await add.getByRole('button', { name: new RegExp(tag) }).click()
		await expect(give.getByRole('listitem').filter({ hasText: tag })).toBeVisible()
	}
	await give.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(`지급: ${people.issued.name}`)

	const items = card(page, '품목').getByRole('listitem')
	await page.getByRole('button', { name: '반납 받기' }).click()
	await page.getByRole('dialog').getByRole('checkbox', { name: /NB-055/ }).uncheck()
	await page.getByRole('dialog').getByRole('button', { name: '반납', exact: true }).click()
	await said(page, '반납 처리했습니다.')
	await expect(items.filter({ hasText: 'NB-054' })).toContainText('반납')
	await expect(items.filter({ hasText: 'NB-055' })).toContainText('사용 중')
	await expect(page.getByText('진행 중', { exact: true })).toBeVisible()

	// What is left is all the second return offers.
	await page.getByRole('button', { name: '반납 받기' }).click()
	await expect(page.getByRole('dialog').getByRole('checkbox')).toHaveCount(1)
	await page.getByRole('dialog').getByRole('button', { name: '반납', exact: true }).click()
	await said(page, '반납 처리했습니다.')
	await expect(items.filter({ hasText: 'NB-055' })).toContainText('반납')
	await expect(page.getByText('반납 완료', { exact: true })).toBeVisible()
})
