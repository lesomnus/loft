import { expect, field, people, said, signIn, test, unique, workspace } from './loft.js'

test('a member reports a fault on an asset, and it is on the work list', async ({ page }) => {
	const symptom = unique('e2e 화면이 깜빡임')
	await signIn(page, people.reporter.login)

	await page.goto(`${workspace}/work`)
	await page.getByRole('button', { name: '+ 고장 신고' }).click()
	const report = page.getByRole('dialog')
	// The demo's MN-003 is this member's monitor.
	const asset = field(report, '자산')
	await asset.getByRole('searchbox').fill('MN-003')
	await asset.getByRole('button', { name: /MN-003/ }).click()
	// And picked, it stays picked: a click the picker redraws under was once
	// handed on to 바꾸기, which unpicked it (loft#53).
	await expect(asset.getByRole('button', { name: '바꾸기', exact: true })).toBeVisible()
	await expect(asset).toContainText('MN-003')
	await field(report, '증상').fill(symptom)
	await report.getByRole('button', { name: '신고', exact: true }).click()
	await said(page, '신고했습니다. 담당자에게 전달됩니다.')

	await expect(page.getByRole('row').filter({ hasText: symptom })).toContainText('MN-003')
})
