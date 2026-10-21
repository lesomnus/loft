import { card, expect, field, people, said, signIn, test, unique, workspace } from './rove.js'

// The access record reviewed (rove#136): who read whose data, a month at a
// time, and that somebody looked it over -- what PIPA's safety measures ask
// of a workspace every month.

test("a workspace's access record is read a month at a time, and its review recorded", async ({ page }) => {
	const note = unique('e2e: 이상 없음')
	await signIn(page, people.reviewer.login)

	// Something to find: the list of people, which shows everybody's name.
	await page.goto(`${workspace}/people`)
	await expect(page.getByText(people.member.name, { exact: true }).first()).toBeVisible()

	await page.goto(`${workspace}/settings?tab=access`)
	const record = page.locator('section').filter({ has: page.getByRole('heading', { name: /^접속기록 — / }) })
	// It opens on last month, which is the one usually reviewed.
	await record.getByRole('button', { name: '다음 달 ›' }).click()
	const now = new Intl.DateTimeFormat('ko-KR', { timeZone: 'Asia/Seoul', year: 'numeric', month: 'long' }).format(new Date())
	await expect(record.getByRole('heading', { name: `접속기록 — ${now}` })).toBeVisible()
	await record.getByLabel('누가').selectOption({ label: people.reviewer.name })
	await record.getByLabel('누구의 정보').selectOption({ label: people.member.name })

	// A read is written a second or so after the call.
	const mine = record.getByRole('row').filter({ hasText: 'PartyService/List' })
	await expect(async () => {
		await record.getByRole('button', { name: '새로고침' }).click()
		await expect(mine.first()).toBeVisible({ timeout: 1000 })
	}).toPass()
	await expect(mine.first()).toContainText(people.reviewer.name)
	await expect(mine.first()).toContainText('조회')
	await expect(mine.first()).toContainText(people.member.name)

	// And looked over.
	const reviews = card(page, '점검 기록')
	await field(reviews, '점검 결과').fill(note)
	await reviews.getByRole('button', { name: `${now} 점검 완료로 기록` }).click()
	await said(page, '점검을 기록했습니다.')
	const done = reviews.getByRole('row').filter({ hasText: note })
	await expect(done).toContainText(people.reviewer.name)
})
