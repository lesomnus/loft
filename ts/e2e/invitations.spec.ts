import { readFile } from 'node:fs/promises'

import { choose, expect, field, owner, said, signIn, test, unique } from './loft.js'

// Invitations (D30): the people who run a workspace invite an address into it,
// the mail says who asked and where to sign in, and they see what they sent
// and take it back. Nobody answers one here any more: who is a member is who
// signs in as a holder of the tenant at roster (D85), and the rest goes with
// #332.

test('an invitation is sent and mailed, and seen and taken back by the people who run the workspace', async ({ page, baseURL }) => {
	const to = `${unique('e2e-invited').replace(' ', '-')}@example.com`
	await signIn(page, owner.login, owner.password)

	await page.getByRole('navigation').getByRole('link', { name: /사람/ }).click()
	await page.getByRole('button', { name: '+ 초대' }).click()
	const invite = page.getByRole('dialog')
	await field(invite, '이메일').fill(to)
	await choose(field(invite, '역할'), '매니저')
	await invite.getByRole('button', { name: '초대 보내기' }).click()
	await said(page, `${to}에 초대를 보냈습니다.`)

	// The mail: who asked, into what, and where to sign in.
	const mail = await mailed(baseURL!, to)
	expect(mail).toContain(`${owner.name}님이 Loft의`)
	expect(mail).toContain('매니저로 초대했습니다')
	expect(mail).toContain(baseURL!)

	await page.getByRole('tab', { name: '초대' }).click()
	const sent = page.getByRole('listitem').filter({ hasText: to })
	await expect(sent).toContainText('기다리는 중')

	// Taken back.
	await sent.getByRole('button', { name: '초대 취소', exact: true }).click()
	await page.getByRole('dialog').getByRole('button', { name: '초대 취소', exact: true }).click()
	await said(page, '취소했습니다.')
	await expect(sent).toContainText('취소함')
})

/**
 * The last mail to `to`, as the server's log says it. The server names no
 * mail server, so it writes each mail to its log instead (server/mail), and
 * the log is where a test reads its mail: `e2e/serve.sh` keeps it in
 * test-results/loft-PORT.
 */
async function mailed(baseURL: string, to: string): Promise<string> {
	const log = `test-results/loft-${new URL(baseURL).port}/loft.log`
	let mail: string | undefined
	await expect
		.poll(
			async () => {
				mail = (await readFile(log, 'utf8')).split('\n').filter((l) => l.includes('mail: not sent') && l.includes(`to=${to} `)).at(-1)
				return mail
			},
			{ message: `a mail to ${to} in ${log}` },
		)
		.toBeDefined()
	return mail!
}
