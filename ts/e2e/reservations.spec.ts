import type { Locator } from '@playwright/test'

import { card, choose, expect, field, idOf, momentAt, owner, people, refusal, refused, rpc, said, signIn, test, unique, weekdayAt, workspace } from './rove.js'

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

test('a booking says in its history who booked it, who approved it and why, and that it was picked up, to its booker and to a manager, wherever they open it, and to nobody else', async ({ page, as }) => {
	const title = unique('e2e 이력 촬영')
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const name = unique('e2e 이력 카메라')

	// A camera of its own that asks a manager first, booked by a member: made
	// through the server, as what is done with it below is, since the forms
	// that do those are the other tests'.
	const admin = await as(owner.login, owner.password)
	const camera = await rpc(admin, 'AssetService/Add', { name, kind: 'item', tag: `EH-${n}` })
	await rpc(admin, 'BookableService/Add', { asset: { id: camera.id }, approval: true })
	await signIn(page, people.historian.login)
	const [from, to] = [momentAt(44, 9), momentAt(44, 12)]
	const booked = await rpc(page, 'ReservationService/Add', { name: title, items: [{ resource: { id: camera.id } }], beginsAt: from, endsAt: to })
	const booking = `예약: ${name} ${from.slice(5, 10)} 09:00`

	// A manager opens the request where it waits for them, and reads who
	// booked what for when.
	const manager = await as(people.approver.login)
	await manager.goto(`${workspace}/reservations?tab=approve`)
	await card(manager, '승인 대기').getByRole('listitem').filter({ hasText: title }).getByRole('button', { name: title }).click()
	const asked = manager.getByRole('dialog').getByRole('list', { name: '이력' }).getByRole('listitem')
	await expect(asked).toHaveCount(1)
	await expect(asked).toContainText(booking)
	await expect(asked).toContainText(people.historian.name)
	await manager.getByRole('dialog').getByRole('button', { name: '닫기' }).click()

	// It is approved, with a reason, and picked up.
	await rpc(manager, 'ReservationService/Approve', { ref: { id: booked.id }, reason: 'e2e 승인 사유' })
	const party = (booked.party as { id: string }).id
	await rpc(manager, 'CustodyService/Add', { party: { id: party }, dueAt: to, reservationId: booked.id, lines: [{ asset: { id: camera.id } }] })

	// Its booker opens it, and reads what happened to it, newest first, by
	// whom: the pickup, the approval and why, and the booking.
	const read = async (p: typeof page) => {
		const dialog = p.getByRole('dialog')
		const lines = dialog.getByRole('list', { name: '이력' }).getByRole('listitem')
		await expect(lines).toHaveCount(3)
		await expect(lines.nth(0)).toContainText(`대여: ${people.historian.name}`)
		await expect(lines.nth(0)).toContainText(people.approver.name)
		await expect(lines.nth(1)).toContainText(`예약 승인: ${title} — e2e 승인 사유`)
		await expect(lines.nth(1)).toContainText(people.approver.name)
		await expect(lines.nth(2)).toContainText(booking)
		await expect(lines.nth(2)).toContainText(people.historian.name)
		await dialog.getByRole('button', { name: '닫기' }).click()
		await expect(dialog).toHaveCount(0)
	}
	await page.goto(`${workspace}/reservations?tab=mine`)
	await page.getByRole('row').filter({ hasText: title }).click()
	await read(page)

	// And so does a manager, from the whole list.
	await manager.goto(`${workspace}/reservations?tab=all`)
	await manager.getByRole('row').filter({ hasText: title }).click()
	await read(manager)

	// Nobody else is told it.
	const other = await as(people.booker.login)
	expect(await refusal(other, 'ReservationService/Timeline', { ref: { id: booked.id } })).toContain('다른 사람의 예약 이력은 볼 수 없습니다')
})

test('a kit booking waits for a member the kit is given that is booked elsewhere, says so wherever it is acted on, and takes it once it is free', async ({ page }) => {
	const talk = unique('e2e 발표')
	const shoot = unique('e2e 촬영')
	await signIn(page, people.approver.login)

	// The projector for a morning five weeks out, and the demo's camera kit
	// for all of it. Nothing else books either.
	const book = async (what: string, title: string, from: number, to: number) => {
		await page.goto(`${workspace}/reservations`)
		await page.getByRole('button', { name: '+ 예약' }).click()
		const form = page.getByRole('dialog')
		await choose(field(form, '무엇을'), what)
		await field(form, '제목').fill(title)
		await field(form, '시작').fill(weekdayAt(35, from))
		await field(form, '끝').fill(weekdayAt(35, to))
		await form.getByRole('button', { name: '예약', exact: true }).click()
		await expect(form).toHaveCount(0)
	}
	await book('빔프로젝터 1', talk, 10, 12)
	await book('촬영 키트', shoot, 9, 13)

	// And then the kit needs the projector: the change is made, and the
	// projector is not taken from the booking that has it.
	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill('PJ-001')
	await page.getByRole('link', { name: '빔프로젝터 1', exact: true }).click()
	await page.getByRole('button', { name: '키트·그룹 연결' }).click()
	const relate = page.getByRole('dialog')
	const target = field(relate, '대상')
	await target.getByRole('searchbox').fill('KT-001')
	await target.getByRole('button', { name: /KT-001/ }).click()
	await relate.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '연결했습니다.')

	// The kit's booking says so in the list, opened, and at its pickup.
	await page.goto(`${workspace}/reservations?tab=mine`)
	const shooting = page.getByRole('row').filter({ hasText: shoot })
	await expect(shooting).toContainText('구성품 충돌')
	await shooting.click()
	const waits = page.getByRole('dialog').getByRole('note', { name: '구성품 충돌' })
	await expect(waits).toContainText('PJ-001')
	await expect(waits).toContainText(talk)
	await page.getByRole('dialog').getByRole('button', { name: '수령 처리 (대여)' }).click()
	await expect(page.getByRole('dialog').getByRole('note', { name: '구성품 충돌' })).toContainText('PJ-001')
	await page.getByRole('dialog').getByRole('button', { name: '취소', exact: true }).click()
	await expect(page.getByRole('dialog')).toHaveCount(0)

	// Once the projector is let go, the kit's booking has it.
	await page.getByRole('row').filter({ hasText: talk }).click()
	await page.getByRole('dialog').getByRole('button', { name: '취소', exact: true }).click()
	await said(page, '취소했습니다.')
	await expect(shooting).not.toContainText('구성품 충돌')
	await shooting.click()
	await expect(page.getByRole('dialog').getByRole('note', { name: '구성품 충돌' })).toHaveCount(0)
})

test('a kit is picked up with the members its booking holds, and without one it waits for, as a loan due back when the booking ends', async ({ page, as }) => {
	const shoot = unique('e2e 수령')
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const tag = { kit: `EK-${n}`, camera: `EC-${n}`, light: `EL-${n}` }

	// A kit of its own with a camera in it, and a light that is booked on its
	// own for the time the kit is, which the kit is given after. Made through
	// the server: the forms that make them are the other tests'.
	const admin = await as(owner.login, owner.password)
	const kit = await rpc(admin, 'AssetService/Add', { name: unique('e2e 수령 키트'), kind: 'kit', tag: tag.kit })
	const camera = await rpc(admin, 'AssetService/Add', { name: 'e2e 수령 카메라', kind: 'item', tag: tag.camera })
	const light = await rpc(admin, 'AssetService/Add', { name: 'e2e 수령 조명', kind: 'item', tag: tag.light })
	await rpc(admin, 'BookableService/Add', { asset: { id: kit.id } })
	await rpc(admin, 'BookableService/Add', { asset: { id: light.id } })
	await rpc(admin, 'AssetService/Relate', { ref: { id: camera.id }, target: { id: kit.id }, kind: 'member_of', required: true })

	await signIn(page, people.approver.login)
	const [from, to] = [momentAt(42, 9), momentAt(42, 12)]
	await rpc(page, 'ReservationService/Add', { name: shoot, items: [{ resource: { id: kit.id } }], beginsAt: from, endsAt: to })
	await rpc(page, 'ReservationService/Add', { name: unique('e2e 조명'), items: [{ resource: { id: light.id } }], beginsAt: from, endsAt: to })
	await rpc(admin, 'AssetService/Relate', { ref: { id: light.id }, target: { id: kit.id }, kind: 'member_of', required: true })

	// Its pickup hands out the camera the booking holds, and says the light
	// is held elsewhere rather than putting it in.
	await page.goto(`${workspace}/reservations?tab=mine`)
	await page.getByRole('row').filter({ hasText: shoot }).click()
	await page.getByRole('dialog').getByRole('button', { name: '수령 처리 (대여)' }).click()
	const pickup = page.getByRole('dialog')
	const out = pickup.getByRole('list', { name: '건넬 자산' })
	await expect(out.getByRole('listitem')).toHaveCount(1)
	await expect(out).toContainText(tag.camera)
	const waits = pickup.getByRole('note', { name: '구성품 충돌' })
	await expect(waits).toContainText(tag.light)
	await expect(waits).toContainText('건넬 자산에 넣지 않았습니다')

	// What it hands out is lent until the booking ends, as its button says.
	await expect(field(pickup, '구분')).toHaveValue('loan')
	await expect(field(pickup, '반납 기한')).toHaveValue(to.slice(0, 16))
	await pickup.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(`대여: ${people.approver.name}`)
})

test('a pool books as many as its members in use, the booking made last waits once one is disposed of until it has one again, and each is picked up as a camera of its own', async ({ page }) => {
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const tag = { pool: `EP-${n}`, one: `EQ-${n}`, two: `ER-${n}`, three: `ES-${n}` }
	const name = unique('e2e 카메라 풀')
	const [first, later] = [unique('e2e 풀 촬영'), unique('e2e 풀 인터뷰')]

	// A group of two cameras of its own, made through the server: the forms
	// that make and relate them are the other tests'.
	await signIn(page, owner.login, owner.password)
	const pool = await rpc(page, 'AssetService/Add', { name, kind: 'group', tag: tag.pool })
	for (const t of [tag.one, tag.two]) {
		const camera = await rpc(page, 'AssetService/Add', { name: `e2e 풀 카메라 ${t}`, kind: 'item', tag: t })
		await rpc(page, 'AssetService/Relate', { ref: { id: camera.id }, target: { id: pool.id }, kind: 'member_of' })
	}

	// Made reservable from its page, as a pool of what is in it.
	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill(tag.pool)
	await page.getByRole('link', { name, exact: true }).click()
	await page.getByRole('button', { name: '예약 가능하게' }).click()
	const make = page.getByRole('dialog')
	await expect(make).toContainText('그룹은 풀입니다')
	await make.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '이제 예약할 수 있습니다.')

	// Both of them booked for one morning six weeks out, one after the other.
	const [from, to] = [momentAt(43, 9), momentAt(43, 12)]
	for (const title of [first, later]) {
		await rpc(page, 'ReservationService/Add', { name: title, items: [{ resource: { id: pool.id } }], beginsAt: from, endsAt: to })
	}

	// One of the cameras disposed of: the pool has one, and the booking made
	// last waits -- in the list, and opened.
	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill(tag.two)
	await page.getByRole('link', { name: `e2e 풀 카메라 ${tag.two}`, exact: true }).click()
	await page.getByRole('button', { name: '정보·상태 변경' }).click()
	const set = page.getByRole('dialog')
	await choose(field(set, '상태'), '폐기')
	await set.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '바꿨습니다.')

	await page.goto(`${workspace}/reservations?tab=mine`)
	const waiting = page.getByRole('row').filter({ hasText: later })
	await expect(waiting).toContainText('수량 부족')
	await expect(page.getByRole('row').filter({ hasText: first })).not.toContainText('수량 부족')
	await waiting.click()
	const short = page.getByRole('dialog').getByRole('note', { name: '수량 부족' })
	await expect(short).toContainText(tag.pool)
	await expect(short).toContainText('남은 것이 없습니다')
	await expect(short).toContainText(first)
	await page.getByRole('dialog').getByRole('button', { name: '닫기' }).click()
	await expect(page.getByRole('dialog')).toHaveCount(0)

	// Given another camera, the pool has two again, and the booking that
	// waited has one.
	const three = await rpc(page, 'AssetService/Add', { name: `e2e 풀 카메라 ${tag.three}`, kind: 'item', tag: tag.three })
	await rpc(page, 'AssetService/Relate', { ref: { id: three.id }, target: { id: pool.id }, kind: 'member_of' })
	await page.reload()
	await expect(waiting).not.toContainText('수량 부족')

	// The first booking picked up: it is offered the first of the cameras
	// there are, by tag, and the other may be ticked instead (D46).
	await page.getByRole('row').filter({ hasText: first }).click()
	await page.getByRole('dialog').getByRole('button', { name: '수령 처리 (대여)' }).click()
	const pickup = page.getByRole('dialog')
	const out = pickup.getByRole('list', { name: '건넬 자산' })
	await expect(out.getByRole('listitem')).toHaveCount(1)
	await expect(out).toContainText(tag.one)
	const units = pickup.getByRole('group', { name: `${name}에서 1개` })
	await expect(units.getByRole('checkbox')).toHaveCount(2)
	await expect(units.getByRole('checkbox', { name: tag.one })).toBeChecked()
	await units.getByRole('checkbox', { name: tag.one }).uncheck()
	await units.getByRole('checkbox', { name: tag.three }).check()
	await expect(out.getByRole('listitem')).toHaveCount(1)
	await expect(out).toContainText(tag.three)
	await pickup.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(/^대여: /)

	// The camera handed out is that booking's until it ends: the other's
	// pickup is offered the one left, and not that one.
	await page.goto(`${workspace}/reservations?tab=mine`)
	await page.getByRole('row').filter({ hasText: later }).click()
	await page.getByRole('dialog').getByRole('button', { name: '수령 처리 (대여)' }).click()
	const next = page.getByRole('dialog')
	await expect(next.getByRole('list', { name: '건넬 자산' })).toContainText(tag.one)
	await expect(next.getByRole('group', { name: `${name}에서 1개` }).getByRole('checkbox')).toHaveCount(1)
	await expect(next.getByRole('list', { name: '건넬 자산' })).not.toContainText(tag.three)
})

test('what is disposed of, or found lost by a count, is offered out of its kit; what is kept in it is said of its booking, whose booker is told; and the kit is picked up without them', async ({ page, as }) => {
	const shoot = unique('e2e 처분')
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const tag = { kit: `EK-${n}`, camera: `EC-${n}`, lens: `EL-${n}`, tripod: `ET-${n}`, battery: `EB-${n}`, room: `ES-${n}` }

	// A kit of its own of four, the lens kept in a room of its own, which a
	// count has just found it missing from; and the kit booked.
	const admin = await as(owner.login, owner.password)
	const room = await rpc(admin, 'AssetService/Add', { name: unique('e2e 장비실'), kind: 'space', tag: tag.room })
	const kit = await rpc(admin, 'AssetService/Add', { name: unique('e2e 처분 키트'), kind: 'kit', tag: tag.kit })
	const camera = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 카메라', kind: 'item', tag: tag.camera })
	const lens = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 렌즈', kind: 'item', tag: tag.lens, to: { id: room.id } })
	const tripod = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 삼각대', kind: 'item', tag: tag.tripod })
	const battery = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 배터리', kind: 'item', tag: tag.battery })
	for (const m of [camera, lens, tripod, battery]) {
		await rpc(admin, 'AssetService/Relate', { ref: { id: m.id }, target: { id: kit.id }, kind: 'member_of', required: true })
	}
	await rpc(admin, 'BookableService/Add', { asset: { id: kit.id } })
	const count = await rpc(admin, 'InventoryCountService/Add', { name: unique('e2e 장비실 실사'), scope: { id: room.id } })
	await rpc(admin, 'InventoryCountService/Reconcile', { ref: { id: count.id } })
	await signIn(page, people.approver.login)
	await rpc(page, 'ReservationService/Add', { name: shoot, items: [{ resource: { id: kit.id } }], beginsAt: momentAt(49, 9), endsAt: momentAt(49, 12) })
	const offer = (form: Locator) => field(form, '속한 키트·그룹').getByRole('checkbox', { name: new RegExp(`^${tag.kit} .*에서 빼기`) })

	// The tripod is disposed of, and taken out of the kit in the same
	// change, which the booking let go of it for -- as its history says.
	await page.goto(`${workspace}/assets/${idOf(tripod.id)}`)
	await page.getByRole('button', { name: '정보·상태 변경' }).click()
	const set = page.getByRole('dialog')
	await expect(offer(set)).toHaveCount(0)
	await choose(field(set, '상태'), '폐기')
	await expect(offer(set)).toBeChecked()
	await field(set, '사유').fill('e2e: 파손')
	await set.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '바꿨습니다.')
	const history = page.getByRole('listitem')
	await expect(history.filter({ hasText: `${tag.tripod} 연결 끝 ${tag.kit}` }).filter({ hasText: `${tag.tripod} 속성 변경` })).toBeVisible()
	await expect(history.filter({ hasText: '예약에서 빠짐' }).filter({ hasText: shoot })).toBeVisible()

	// The lens the count never saw is lost, and out of the kit as well.
	await page.goto(`${workspace}/counts/${idOf(count.id)}`)
	await card(page, '결과').getByRole('listitem').filter({ hasText: tag.lens }).getByRole('button', { name: '처리', exact: true }).click()
	const resolve = page.getByRole('dialog')
	await expect(field(resolve, '처리')).toHaveValue('lost')
	await expect(offer(resolve)).toBeChecked()
	await resolve.getByRole('button', { name: '처리', exact: true }).click()
	await said(page, '처리했습니다.')

	// The battery is retired by the owner, at a desk of their own, and kept
	// in the kit, its box unticked: the booking still holds it.
	await admin.goto(`${workspace}/assets/${idOf(battery.id)}`)
	await admin.getByRole('button', { name: '정보·상태 변경' }).click()
	const retire = admin.getByRole('dialog')
	await choose(field(retire, '상태'), '불용')
	await offer(retire).uncheck()
	await retire.getByRole('button', { name: '저장', exact: true }).click()
	await said(admin, '바꿨습니다.')

	// Whose booking it is hears of it before anybody comes for it: told so,
	// and the booking marked where it is listed and said in its dialog.
	await page.goto(`${workspace}/notifications`)
	const notice = page.getByRole('listitem').filter({ hasText: shoot })
	await expect(notice).toContainText(`예약 자산 불용: ${shoot}`)
	await expect(notice).toContainText(`${tag.battery} e2e 처분 배터리은(는) 불용 상태라 수령할 때 건네지 않습니다`)
	await page.goto(`${workspace}/reservations?tab=mine`)
	const booked = page.getByRole('row').filter({ hasText: shoot })
	await expect(booked).toContainText('건넬 수 없는 자산')
	await booked.click()
	const marked = page.getByRole('dialog').getByRole('note', { name: '건넬 수 없는 자산' })
	await expect(marked).toContainText(`${tag.battery} e2e 처분 배터리: 불용`)
	await expect(marked).toContainText('수령할 때 건네지 않습니다')
	await expect(marked).not.toContainText(tag.tripod)
	await expect(marked).not.toContainText(tag.lens)

	// What the booking hands out now is the camera alone; the battery it
	// holds is out of use, which nothing hands over, and is said instead.
	await page.getByRole('dialog').getByRole('button', { name: '수령 처리 (대여)' }).click()
	const pickup = page.getByRole('dialog')
	const out = pickup.getByRole('list', { name: '건넬 자산' })
	await expect(out.getByRole('listitem')).toHaveCount(1)
	await expect(out).toContainText(tag.camera)
	const gone = pickup.getByRole('note', { name: '건넬 수 없는 자산' })
	await expect(gone).toContainText(`${tag.battery} e2e 처분 배터리: 불용`)
	await expect(gone).toContainText('건넬 자산에 넣지 않았습니다')
	await expect(gone).not.toContainText(tag.tripod)
	await pickup.getByRole('button', { name: '건네기', exact: true }).click()
	await said(page, '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.')
})

test('what is out of use is not booked anew: the booking form says so of a retired item, and booking it is refused, naming it and its status', async ({ page, as }) => {
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const tag = `EU-${n}`
	const name = unique('e2e 불용 카메라')

	// A camera of its own, made reservable and then retired through the
	// server: the forms that do those are the other tests'.
	const admin = await as(owner.login, owner.password)
	const camera = await rpc(admin, 'AssetService/Add', { name, kind: 'item', tag })
	await rpc(admin, 'BookableService/Add', { asset: { id: camera.id } })
	await rpc(admin, 'AssetService/SetAttributes', { ref: { id: camera.id }, set: { status: 'retired' } })

	await signIn(page, people.booker.login)
	await page.goto(`${workspace}/reservations`)
	await page.getByRole('button', { name: '+ 예약' }).click()
	const book = page.getByRole('dialog')
	await choose(field(book, '무엇을'), `${name} (물품) · 불용`)
	await expect(book.getByRole('note', { name: '쓸 수 없는 자산' })).toHaveText('불용 상태라 새로 예약할 수 없습니다.')
	await field(book, '제목').fill(unique('e2e 불용 촬영'))
	await field(book, '시작').fill(weekdayAt(56, 10))
	await field(book, '끝').fill(weekdayAt(56, 11))
	await book.getByRole('button', { name: '예약', exact: true }).click()
	await refused(page, `쓸 수 없는 것은 새로 예약할 수 없습니다: ${tag} ${name}(불용)`)
	await expect(book).toBeVisible()
})

test('a room that goes out of use: its booking says so and its booker is told, it is not checked into, and it is not booked anew', async ({ page, as }) => {
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const tag = `EV-${n}`
	const name = unique('e2e 불용 회의실')
	const talk = unique('e2e 불용 회의')

	// A room of its own, booked from a few minutes on: it may be checked
	// into now.
	const admin = await as(owner.login, owner.password)
	const room = await rpc(admin, 'AssetService/Add', { name, kind: 'space', tag })
	await rpc(admin, 'BookableService/Add', { asset: { id: room.id } })
	await signIn(page, people.booker.login)
	const from = new Date(Date.now() + 5 * 60_000)
	const to = new Date(from.getTime() + 60 * 60_000)
	const r = await rpc(page, 'ReservationService/Add', { name: talk, items: [{ resource: { id: room.id } }], beginsAt: from.toISOString(), endsAt: to.toISOString() })
	await page.goto(`${workspace}/reservations?tab=mine`)
	const booked = page.getByRole('row').filter({ hasText: talk })
	await booked.click()
	await expect(page.getByRole('dialog').getByRole('button', { name: '체크인' })).toBeVisible()
	await page.getByRole('dialog').getByRole('button', { name: '닫기' }).click()

	// The owner retires it, at a desk of their own.
	await admin.goto(`${workspace}/assets/${idOf(room.id)}`)
	await admin.getByRole('button', { name: '정보·상태 변경' }).click()
	const retire = admin.getByRole('dialog')
	await choose(field(retire, '상태'), '불용')
	await retire.getByRole('button', { name: '저장', exact: true }).click()
	await said(admin, '바꿨습니다.')

	// Whose booking it is is told, and the booking is marked where it is
	// listed and said in its dialog, which offers no check-in -- nor does the
	// server check it in.
	await page.goto(`${workspace}/notifications`)
	const notice = page.getByRole('listitem').filter({ hasText: talk })
	await expect(notice).toContainText(`예약 공간 불용: ${talk}`)
	await expect(notice).toContainText(`${tag} ${name}은(는) 불용 상태라 체크인할 수 없습니다`)
	await page.goto(`${workspace}/reservations?tab=mine`)
	await expect(booked).toContainText('쓸 수 없는 공간')
	await booked.click()
	const open = page.getByRole('dialog')
	const marked = open.getByRole('note', { name: '쓸 수 없는 공간' })
	await expect(marked).toContainText(`${tag} ${name}: 불용`)
	await expect(marked).toContainText('체크인할 수 없습니다')
	await expect(open.getByRole('button', { name: '체크인' })).toHaveCount(0)
	await open.getByRole('button', { name: '닫기' }).click()
	expect(await refusal(page, 'ReservationService/CheckIn', { ref: { id: r.id } })).toContain(`체크인할 수 없습니다: ${tag} ${name}(불용)`)

	// And it is not booked anew.
	await page.goto(`${workspace}/reservations`)
	await page.getByRole('button', { name: '+ 예약' }).click()
	const book = page.getByRole('dialog')
	await choose(field(book, '무엇을'), `${name} · 불용`)
	await field(book, '시작').fill(weekdayAt(57, 10))
	await field(book, '끝').fill(weekdayAt(57, 11))
	await book.getByRole('button', { name: '예약', exact: true }).click()
	await refused(page, `쓸 수 없는 것은 새로 예약할 수 없습니다: ${tag} ${name}(불용)`)
})
