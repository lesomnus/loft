import { card, escape, expect, field, people, signIn, test, unique, workspace } from './rove.js'

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

// What a workspace is called is its owner's to say (D58), in settings: the
// switcher says it from then on, and a workspace whose name is taken back is
// called after its owner again (D57). A person's name in it is said to stay
// in its history.
test("a workspace's owner renames it, and takes the name back", async ({ page }) => {
	const made = unique('e2e 이름 지을 곳')
	const renamed = unique('e2e 연구지원팀 장비')
	const owner = people.renames
	await signIn(page, owner.login)

	// A workspace of their own, which they own.
	const switcher = page.locator('summary')
	await switcher.click()
	await page.getByRole('button', { name: '+ 새 워크스페이스' }).click()
	await page.getByPlaceholder('워크스페이스 이름').fill(made)
	await page.getByRole('button', { name: '만들기', exact: true }).click()
	await expect(switcher).toHaveText(made)

	await page.getByRole('navigation').getByRole('link', { name: /설정/ }).click()
	await page.getByRole('tab', { name: '워크스페이스' }).click()
	const named = card(page, '워크스페이스')
	// Saved, the page is drawn again, and says the name from its first draw.
	const save = async (): Promise<void> => {
		const drawn = page.waitForEvent('load')
		await named.getByRole('button', { name: '저장', exact: true }).click()
		await drawn
	}
	await expect(field(named, '이름')).toHaveValue(made)

	await field(named, '이름').fill(renamed)
	await expect(named.getByRole('status')).toHaveCount(0)
	await save()
	await expect(switcher).toHaveText(renamed)
	await expect(field(named, '이름')).toHaveValue(renamed)

	// Somebody of the workspace named in it is told of.
	await field(named, '이름').fill(`${owner.name} 장비`)
	await expect(named.getByRole('status')).toContainText(owner.name)

	// None: called after its owner, here and in the list.
	await field(named, '이름').fill('')
	await expect(named.getByRole('status')).toHaveCount(0)
	await save()
	await expect(switcher).toHaveText(`${owner.name}의 워크스페이스`)
	await switcher.click()
	await expect(page.getByRole('button', { name: new RegExp(`^${escape(owner.name)}의 워크스페이스`) })).toBeVisible()
})
