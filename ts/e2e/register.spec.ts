import { card, expect, field, owner, said, signIn, test, unique, workspace } from './loft.js'

/** A sheet in the form, as a file the page is given. */
function sheet(rows: string[][]): { name: string; mimeType: string; buffer: Buffer } {
	const lines = [['이름', '종류', '유형', '태그', '위치태그', '설명'], ...rows].map((r) => r.join(','))
	return { name: 'intake.csv', mimeType: 'text/csv', buffer: Buffer.from(`${lines.join('\n')}\n`) }
}

test('new assets are registered from a sheet, shown first, as one intake', async ({ page }) => {
	const name = unique('e2e 빔프로젝터')
	const type = unique('e2e 프로젝터')
	const tag = unique('E2E').replace(' ', '-').toUpperCase()
	// The owner: the type the sheet names is one only an admin makes.
	await signIn(page, owner.login, owner.password)

	await page.goto(`${workspace}/assets`)
	await page.getByRole('link', { name: '대량 등록', exact: true }).click()
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('대량 등록')

	const download = page.waitForEvent('download')
	await page.getByRole('button', { name: '양식 내려받기' }).click()
	expect((await download).suggestedFilename()).toBe('loft-대량등록-양식.csv')
	await expect(card(page, '양식')).toContainText('CSV UTF-8')

	// A workbook is refused unopened, saying how to save it as the form is.
	await field(page, '시트').setInputFiles({
		name: 'intake.xlsx',
		mimeType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
		buffer: Buffer.from('PK\x03\x04'),
	})
	await expect(page.getByRole('alert')).toContainText('엑셀 파일은 받지 않습니다')

	// A tag that is an asset's already: said where it is, and nothing registered.
	await field(page, '시트').setInputFiles(sheet([['e2e 중복', '물품', '', 'SP-12', '', '']]))
	const preview = card(page, '미리 보기')
	await expect(preview.getByRole('alert')).toContainText('2행 태그: 태그 SP-12는 이미 있는 자산의 것입니다')
	await expect(preview.getByRole('button', { name: '1개 등록' })).toBeDisabled()

	await field(page, '시트').setInputFiles(
		sheet([
			[name, '물품', type, tag, 'SP-12', 'e2e'],
			[`${name} 2`, '', type, '', 'SP-12', ''],
		]),
	)
	await expect(preview).toContainText('2개를 등록합니다.')
	await expect(preview).toContainText(`새로 만듦: 유형 ${type}`)
	await expect(preview.getByRole('alert')).toHaveCount(0)
	await expect(preview.getByRole('row').filter({ hasText: tag })).toContainText('SP-12')

	await field(preview, '사유').fill('e2e 입고')
	await preview.getByRole('button', { name: '2개 등록' }).click()
	await said(page, '2개를 등록했습니다')

	// In the register, of the type it made, in the store; and its history
	// says it was registered, and by what intake.
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill(tag)
	await page.getByRole('link', { name, exact: true }).click()
	await expect(page.getByRole('heading', { level: 1 })).toContainText(name)
	// The type beside its name: the catalog was read again after the intake
	// made it, rather than when the page was opened.
	await expect(page.getByRole('main')).toContainText(`· ${type}`)
	await expect(card(page, '정보')).toContainText('창고')
	await expect(page.getByRole('listitem').filter({ hasText: `${tag} 등록` })).toContainText('대량 등록: 2개')
})
