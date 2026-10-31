import { choose, expect, field, momentAt, owner, people, refused, rpc, said, signIn, test, unique, weekdayAt, workspace } from './loft.js'

// What is out takes its time (design 4, D53): a thing lent at the desk is not
// booked by anybody for the hours it is out, and a booking made before it was
// lent keeps its time and says, wherever it is opened, that the thing is out.

test('a camera lent at the desk is not booked for the hours it is out, and a booking made before says it is out', async ({ page, as }) => {
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const name = unique('e2e 대여 카메라')
	const tag = `EO-${n}`
	const title = unique('e2e 먼저 한 예약')

	// A camera of its own, booked by a member for a morning nine days out:
	// made through the server, as other tests make what they start from.
	const admin = await as(owner.login, owner.password)
	const camera = await rpc(admin, 'AssetService/Add', { name, kind: 'item', tag })
	await rpc(admin, 'BookableService/Add', { asset: { id: camera.id } })
	const booker = await as(people.booksFirst.login)
	await rpc(booker, 'ReservationService/Add', { name: title, items: [{ resource: { id: camera.id } }], beginsAt: momentAt(9, 10), endsAt: momentAt(9, 12) })

	// The desk lends it until eleven that day. The form says what is booked
	// of it over that time, and it is handed out all the same.
	await signIn(page, owner.login, owner.password)
	await page.goto(`${workspace}/custody`)
	await page.getByRole('button', { name: '+ 지급·대여' }).click()
	const give = page.getByRole('dialog')
	await choose(field(give, '받는 사람'), people.borrows.name)
	await choose(field(give, '구분'), '대여')
	await field(give, '반납 기한').fill(weekdayAt(9, 11))
	const add = field(give, '자산 추가')
	await add.getByRole('searchbox').fill(tag)
	await add.getByRole('button', { name: new RegExp(tag) }).click()
	await expect(give.getByRole('list', { name: '건넬 자산' }).getByRole('listitem')).toContainText(name)
	await expect(give.getByRole('note', { name: '겹치는 예약' })).toContainText(title)
	await give.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')

	// The booking made first keeps its time and says the camera is out then:
	// in the list, and in the booking itself.
	await booker.goto(`${workspace}/reservations?tab=mine`)
	const row = booker.getByRole('row').filter({ hasText: title })
	await expect(row).toContainText('확정')
	await expect(row).toContainText('대여 중')
	await row.click()
	const out = booker.getByRole('dialog').getByRole('note', { name: '나가 있는 자산' })
	await expect(out).toContainText(tag)
	await expect(out).toContainText('대여 중입니다')
	await booker.getByRole('dialog').getByRole('button', { name: '닫기' }).click()

	// And nobody books it for the hours it is out, even where nothing else
	// was booked: the refusal says until when.
	await page.goto(`${workspace}/reservations`)
	await page.getByRole('button', { name: '+ 예약' }).click()
	const book = page.getByRole('dialog')
	await choose(field(book, '무엇을'), name)
	await field(book, '제목').fill(unique('e2e 겹친 예약'))
	await field(book, '시작').fill(weekdayAt(9, 8))
	await field(book, '끝').fill(weekdayAt(9, 9))
	await book.getByRole('button', { name: '예약', exact: true }).click()
	await refused(page, '대여 중')
})
