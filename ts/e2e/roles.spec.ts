import { expect, owner, people, signIn, test } from './loft.js'

// What somebody may do is what the roles roster binds them hold (D89), and the
// page draws its menus, tabs and buttons from the methods `PartyService.Me`
// says they may call -- never from a role's name, which the tenant may change
// at roster. Each of the five roles Loft writes there, as `init --demo` binds
// them, is shown what it may do and nothing else.

/** Every menu, and the settings tabs and buttons that go by what somebody may call. */
const menus = ['대시보드', '자산', '공간', '예약', '지급·대여', '재고', '실사', '작업', '구매', '리포트', '사람', '설정']
const tabs = ['유형', '모델', '예약 자원', '라벨', '작업 기록', '감사 기록', '접속기록', '워크스페이스']

const roles: { role: string; login: string; password?: string; shown: { menus: string[]; tabs: string[]; addType: boolean } }[] = [
	{ role: 'owner', login: owner.login, password: owner.password, shown: { menus, tabs, addType: true } },
	{ role: 'admin', login: people.counts.login, shown: { menus, tabs: tabs.filter((t) => t !== '워크스페이스'), addType: true } },
	{ role: 'manager', login: people.approver.login, shown: { menus, tabs: ['유형', '모델', '예약 자원', '라벨'], addType: false } },
	{
		role: 'auditor',
		login: people.reviewer.login,
		shown: { menus: menus.filter((m) => m !== '구매'), tabs: tabs.filter((t) => t !== '워크스페이스'), addType: false },
	},
	{
		role: 'member',
		login: people.member.login,
		shown: { menus: menus.filter((m) => m !== '구매' && m !== '리포트'), tabs: ['유형', '모델', '예약 자원', '라벨'], addType: false },
	},
]

for (const r of roles) {
	test(`the ${r.role} Loft writes at roster is shown what it may call, and nothing else`, async ({ page }) => {
		await signIn(page, r.login, r.password)
		// A menu is called by its icon and its name.
		const menu = (name: string) => page.getByRole('navigation').getByRole('link', { name: new RegExp(`\\s${name}$`) })
		for (const name of menus) {
			await expect(menu(name), name).toHaveCount(r.shown.menus.includes(name) ? 1 : 0)
		}

		await menu('설정').click()
		await expect(page.getByRole('tab', { name: '유형', exact: true })).toBeVisible()
		for (const name of tabs) {
			await expect(page.getByRole('tab', { name, exact: true }), name).toHaveCount(r.shown.tabs.includes(name) ? 1 : 0)
		}
		await expect(page.getByRole('button', { name: '+ 유형', exact: true })).toHaveCount(r.shown.addType ? 1 : 0)
	})
}
