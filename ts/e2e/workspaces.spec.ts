import { escape, expect, people, signIn, test, unique, workspace } from './rove.js'

test('a second workspace is made, and the switcher moves between the two', async ({ page }) => {
	const name = unique('e2e 실험실')
	await signIn(page, people.workspaces.login)
	// The switcher is the one disclosure on the page, and says where it is.
	const switcher = page.locator('summary')
	const demo = (await switcher.innerText()).trim()

	await switcher.click()
	await page.getByRole('button', { name: '+ 새 워크스페이스' }).click()
	await page.getByPlaceholder('워크스페이스 이름').fill(name)
	await page.getByRole('button', { name: '만들기', exact: true }).click()
	// Its address is made up for it, and is not the demo's.
	await expect(page).toHaveURL(new RegExp(`/@(?!${escape(workspace.slice(2))}/)[^/]+/$`))
	await expect(switcher).toHaveText(name)

	await switcher.click()
	await page.getByRole('button', { name: new RegExp(`^${escape(demo)}`) }).click()
	await expect(page).toHaveURL(new RegExp(`${workspace}/$`))
	await expect(switcher).toHaveText(demo)

	await switcher.click()
	await page.getByRole('button', { name: new RegExp(`^${escape(name)}`) }).click()
	await expect(switcher).toHaveText(name)
})
