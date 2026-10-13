import { card, choose, expect, field, people, said, signIn, test, unique, weekdayAt, workspace } from './rove.js'

test('a booking of a room that needs approval waits for a manager, who approves it', async ({ page, as }) => {
	const title = unique('e2e 분기 회의')
	await signIn(page, people.booker.login)

	await page.goto(`${workspace}/reservations`)
	await page.getByRole('button', { name: '+ 예약' }).click()
	const book = page.getByRole('dialog')
	// 대회의실 is the demo's one room that asks a manager first; three weeks
	// out on a weekday afternoon is inside its hours and clear of the demo's
	// own bookings.
	await choose(field(book, '무엇을'), '대회의실')
	await expect(book.getByText('관리자 승인이 필요한 자원입니다.')).toBeVisible()
	await field(book, '제목').fill(title)
	await field(book, '시작').fill(weekdayAt(21, 14))
	await field(book, '끝').fill(weekdayAt(21, 15))
	await book.getByRole('button', { name: '예약', exact: true }).click()
	await expect(book).toHaveCount(0)

	await page.getByRole('tab', { name: '내 예약' }).click()
	const mine = page.getByRole('row').filter({ hasText: title })
	await expect(mine).toContainText('승인 대기')

	const manager = await as(people.approver.login)
	await manager.goto(`${workspace}/reservations`)
	await manager.getByRole('tab', { name: '승인' }).click()
	const asked = card(manager, '승인 대기').getByRole('listitem').filter({ hasText: title })
	await asked.getByRole('button', { name: '승인', exact: true }).click()
	await said(manager, '승인했습니다.')
	await expect(asked).toHaveCount(0)

	await page.reload()
	await expect(mine).toContainText('확정')
})
