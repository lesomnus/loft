import { printed } from './qr.js'
import { card, choose, expect, field, people, said, signIn, test, unique, workspace } from './rove.js'

// M4's self-service (plan.md 5장): what is handed over is confirmed by the
// person it went to, who scans its label -- 스캔 → 인수 확인 -- with nobody
// else in the room.

test('a member scans the label on what was handed over, and confirms they have it', async ({ page, as }) => {
	const memo = unique('e2e 입사 지급')
	const member = people.selfService
	await signIn(page, people.custody.login)

	// Handed over, and labelled, by a manager.
	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill('PH-004')
	await page.getByRole('link', { name: '업무폰 4', exact: true }).click()
	await expect(page.getByRole('heading', { level: 1 })).toContainText('PH-004')
	const asset = page.url()
	await page.getByRole('button', { name: '지급·대여', exact: true }).click()
	const give = page.getByRole('dialog')
	await choose(field(give, '받는 사람'), member.name)
	await field(give, '메모').fill(memo)
	await give.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')
	const custody = page.url()
	await expect(card(page, '내용')).toContainText(/인수 확인\s*대기/)

	await page.goto(asset)
	await page.getByRole('link', { name: '라벨 인쇄' }).click()
	const [label] = await printed(page)
	expect(label.url).toMatch(/\/l\/[0-9A-Z]+$/)

	// The member, with their phone: the camera opens what the label says.
	const theirs = await as(member.login)
	await theirs.goto(label.url)
	const head = theirs.getByRole('heading', { level: 1 })
	await expect(head).toContainText('PH-004')
	await expect(card(theirs, '정보')).toContainText(new RegExp(`사용자\\s*${member.name}`))
	await theirs.getByRole('button', { name: '인수 확인', exact: true }).click()
	await said(theirs, '인수를 확인했습니다.')
	await expect(theirs.getByRole('button', { name: '인수 확인', exact: true })).toHaveCount(0)

	// Nothing of it waits for them any more,
	await theirs.goto(`${workspace}/me`)
	await expect(card(theirs, '인수 확인이 필요한 것')).not.toContainText(memo)
	// and the manager sees when it was confirmed.
	await page.goto(custody)
	await expect(card(page, '내용')).not.toContainText(/인수 확인\s*대기/)
	await expect(page.getByRole('button', { name: '인수 확인', exact: true })).toHaveCount(0)
})
