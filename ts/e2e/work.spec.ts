import { expect, field, people, said, signIn, test, unique, workspace } from './rove.js'

test('a member reports a fault on an asset, and it is on the work list', async ({ page }) => {
	const symptom = unique('e2e 화면이 깜빡임')
	await signIn(page, people.reporter.login)

	await page.goto(`${workspace}/work`)
	await page.getByRole('button', { name: '+ 고장 신고' }).click()
	const report = page.getByRole('dialog')
	// The demo's MN-003 is this member's monitor.
	await report.getByPlaceholder('태그, 이름, 시리얼로 찾기').fill('MN-003')
	await report.getByRole('button', { name: /MN-003/ }).click()
	// And picked, it stays picked (pickers.tsx). Its 바꾸기 button is named by
	// the form's label it sits in, so it is found by what it says.
	await expect(report.getByText('바꾸기', { exact: true })).toBeVisible()
	await field(report, '증상').fill(symptom)
	await report.getByRole('button', { name: '신고', exact: true }).click()
	await said(page, '신고했습니다. 담당자에게 전달됩니다.')

	await expect(page.getByRole('row').filter({ hasText: symptom })).toContainText('MN-003')
})
