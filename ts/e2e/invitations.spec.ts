import { readFile } from 'node:fs/promises'

import { choose, escape, expect, field, people, said, signIn, test, unique } from './loft.js'

// Invitations (D30): the people who run a workspace invite an address into it;
// the mail carries the way in; the account that opens it says yes or no, and
// the workspace sees which.

test('an invitation is sent, received by the link its mail carries, and accepted; another is denied', async ({ page, as, baseURL }) => {
	const name = unique('e2e 초대하는 곳')
	await signIn(page, people.inviter.login)

	// A workspace of the inviter's own, which nobody else is in yet.
	const switcher = page.locator('summary')
	await switcher.click()
	await page.getByRole('button', { name: '+ 새 워크스페이스' }).click()
	await page.getByPlaceholder('워크스페이스 이름').fill(name)
	await page.getByRole('button', { name: '만들기', exact: true }).click()
	await expect(switcher).toHaveText(name)
	const home = page.url()

	await page.getByRole('navigation').getByRole('link', { name: /사람/ }).click()
	for (const [who, role] of [
		[people.invitee, '구성원'],
		[people.declines, '매니저'],
	] as const) {
		await page.getByRole('button', { name: '+ 초대' }).click()
		const invite = page.getByRole('dialog')
		await field(invite, '이메일').fill(address(who))
		await choose(field(invite, '역할'), role)
		await invite.getByRole('button', { name: '초대 보내기' }).click()
		await said(page, `${address(who)}에 초대를 보냈습니다.`)
	}
	await page.getByRole('tab', { name: '초대' }).click()
	const sent = page.getByRole('listitem')
	await expect(sent.filter({ hasText: address(people.invitee) })).toContainText('기다리는 중')
	await expect(sent.filter({ hasText: address(people.declines) })).toContainText('기다리는 중')

	// The one it is for opens the link in their mail, signed in, and says yes.
	const yes = await as(people.invitee.login)
	await yes.goto(await mailed(baseURL!, address(people.invitee)))
	await expect(yes.getByRole('heading', { name: '받은 초대' })).toBeVisible()
	const offer = yes.getByRole('listitem').filter({ hasText: name })
	await expect(offer).toContainText(`${people.inviter.name}님이 구성원으로 초대했습니다`)
	await offer.getByRole('button', { name: '수락', exact: true }).click()
	await expect(yes).toHaveURL(new RegExp(`^${escape(home)}$`))
	await expect(yes.locator('summary')).toHaveText(name)

	// The other says no, and nothing is left waiting for them.
	const no = await as(people.declines.login)
	await no.goto(await mailed(baseURL!, address(people.declines)))
	const other = no.getByRole('listitem').filter({ hasText: name })
	await expect(other).toContainText(`${people.inviter.name}님이 매니저로 초대했습니다`)
	await other.getByRole('button', { name: '거절', exact: true }).click()
	await expect(no.getByText('기다리는 초대가 없습니다.')).toBeVisible()

	// What became of each, where it was sent from.
	await page.reload()
	await page.getByRole('tab', { name: '초대' }).click()
	await expect(sent.filter({ hasText: address(people.invitee) })).toContainText('수락함')
	await expect(sent.filter({ hasText: address(people.declines) })).toContainText('거절함')
	// And who is in it now: the one who said yes, as what they were asked to be.
	await page.getByRole('tab', { name: '조직도' }).click()
	await expect(page.getByRole('listitem').filter({ hasText: people.invitee.name })).toContainText('구성원')
	await expect(page.getByRole('listitem').filter({ hasText: people.declines.name })).toHaveCount(0)
})

/** The address `--demo` gives a person. */
function address(p: { login: string }): string {
	return `${p.login}@example.com`
}

/**
 * The link the last invitation mailed to `to` carries. The server names no
 * mail server, so it writes each mail to its log instead (server/mail), and
 * the log is where a test reads its mail: `e2e/serve.sh` keeps it in
 * test-results/loft-PORT.
 */
async function mailed(baseURL: string, to: string): Promise<string> {
	const log = `test-results/loft-${new URL(baseURL).port}/loft.log`
	let link: string | undefined
	await expect
		.poll(
			async () => {
				const mails = (await readFile(log, 'utf8')).split('\n').filter((l) => l.includes('mail: not sent') && l.includes(`to=${to} `))
				link = mails.at(-1)?.match(/https?:\/\/[^\s\\"]+\/invitations\?code=[A-Za-z0-9_-]+/)?.[0]
				return link
			},
			{ message: `a mail to ${to} in ${log}` },
		)
		.toBeDefined()
	return link!
}
