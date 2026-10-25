import type { Locator } from '@playwright/test'

import { card, choose, expect, field, idOf, momentAt, owner, people, rpc, said, signIn, test, unique, weekdayAt, workspace } from './rove.js'

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

test('a kit is picked up with the members its booking holds, and without one it waits for', async ({ page, as }) => {
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
})

test('what is disposed of, or found lost by a count, is offered out of its kit, and the kit is picked up without it', async ({ page, as }) => {
	const shoot = unique('e2e 처분')
	const n = Math.random().toString(36).slice(2, 7).toUpperCase()
	const tag = { kit: `EK-${n}`, camera: `EC-${n}`, lens: `EL-${n}`, tripod: `ET-${n}`, room: `ES-${n}` }

	// A kit of its own of three, the lens kept in a room of its own, which a
	// count has just found it missing from; and the kit booked.
	const admin = await as(owner.login, owner.password)
	const room = await rpc(admin, 'AssetService/Add', { name: unique('e2e 장비실'), kind: 'space', tag: tag.room })
	const kit = await rpc(admin, 'AssetService/Add', { name: unique('e2e 처분 키트'), kind: 'kit', tag: tag.kit })
	const camera = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 카메라', kind: 'item', tag: tag.camera })
	const lens = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 렌즈', kind: 'item', tag: tag.lens, to: { id: room.id } })
	const tripod = await rpc(admin, 'AssetService/Add', { name: 'e2e 처분 삼각대', kind: 'item', tag: tag.tripod })
	for (const m of [camera, lens, tripod]) {
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

	// What the booking hands out now is the camera alone.
	await page.goto(`${workspace}/reservations?tab=mine`)
	await page.getByRole('row').filter({ hasText: shoot }).click()
	await page.getByRole('dialog').getByRole('button', { name: '수령 처리 (대여)' }).click()
	const out = page.getByRole('dialog').getByRole('list', { name: '건넬 자산' })
	await expect(out.getByRole('listitem')).toHaveCount(1)
	await expect(out).toContainText(tag.camera)
})
