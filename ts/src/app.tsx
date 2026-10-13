/**
 * The frame every page is drawn in, and which page is where.
 *
 * @module
 */

import { useState, type ReactNode } from 'react'
import { BrowserRouter, NavLink, Navigate, Route, Routes, useNavigate } from 'react-router-dom'

import { NotificationService, workspace, type Account } from './api.js'
import { CatalogProvider, useCatalog } from './catalog.js'
import { AssetPage } from './pages/asset.js'
import { Assets } from './pages/assets.js'
import { CountPage, Counts } from './pages/counts.js'
import { Custodies, CustodyPage } from './pages/custody.js'
import { Dashboard } from './pages/dashboard.js'
import { LabelSheet } from './pages/labels.js'
import { Me } from './pages/me.js'
import { Notifications } from './pages/notifications.js'
import { People } from './pages/people.js'
import { Purchases } from './pages/purchases.js'
import { Reservations } from './pages/reservations.js'
import { Scan } from './pages/scan.js'
import { Settings } from './pages/settings.js'
import { Spaces } from './pages/spaces.js'
import { Stocks } from './pages/stock.js'
import { Work } from './pages/work.js'
import { Offline, useRpc } from './rpc.js'
import { roleWord } from './words.js'
import { Switcher } from './workspaces.js'

const nav: { to: string; label: string; icon: string; min?: 'manager' | 'admin' }[] = [
	{ to: '/', label: '대시보드', icon: '◎' },
	{ to: '/assets', label: '자산', icon: '▦' },
	{ to: '/spaces', label: '공간', icon: '⌂' },
	{ to: '/reservations', label: '예약', icon: '◷' },
	{ to: '/custody', label: '지급·대여', icon: '⇄' },
	{ to: '/stock', label: '재고', icon: '▤' },
	{ to: '/counts', label: '실사', icon: '✓' },
	{ to: '/work', label: '작업', icon: '⚒' },
	{ to: '/purchases', label: '구매', icon: '₩', min: 'manager' },
	{ to: '/people', label: '사람', icon: '☺' },
	{ to: '/settings', label: '설정', icon: '⚙' },
]

function Bell(): ReactNode {
	const inbox = useRpc(NotificationService.method.inbox, { unread: true, size: 1 }, { poll: 60_000 })
	const n = inbox.data?.unread ?? 0
	return (
		<NavLink to="/notifications" className="bell" title="알림">
			🔔{n > 0 && <span className="dot">{n > 99 ? '99+' : n}</span>}
		</NavLink>
	)
}

function Frame(props: { account: Account; onSignOut: () => void; children: ReactNode }): ReactNode {
	const c = useCatalog()
	const go = useNavigate()
	const [q, setQ] = useState('')
	const [open, setOpen] = useState(false)
	const name = c.me.party?.name ?? c.me.holder?.name ?? c.me.holder?.alias ?? ''

	return (
		<div className={`frame ${open ? 'nav-open' : ''}`}>
			<aside className="side" onClick={() => setOpen(false)}>
				<div className="brand">
					<img src="/icon.svg" alt="" />
					<div>
						<strong>Rove</strong>
					</div>
				</div>
				<div onClick={(e) => e.stopPropagation()}>
					<Switcher account={props.account} current={c.me.tenant?.name || c.me.tenant?.alias || workspace} />
				</div>
				<nav>
					{nav
						.filter((n) => n.min === undefined || c.can(n.min))
						.map((n) => (
							<NavLink key={n.to} to={n.to} end={n.to === '/'}>
								<span className="icon">{n.icon}</span>
								{n.label}
							</NavLink>
						))}
				</nav>
				<div className="who">
					<NavLink to="/me">
						<strong>{name}</strong>
						<span>{roleWord[c.role] ?? c.role}</span>
					</NavLink>
					<button className="link" onClick={props.onSignOut}>
						로그아웃
					</button>
				</div>
			</aside>
			<div className="main">
				<header className="top">
					<button className="icon burger" onClick={() => setOpen((v) => !v)} aria-label="메뉴">
						☰
					</button>
					<form
						className="global-search"
						onSubmit={(e) => {
							e.preventDefault()
							go(`/assets?q=${encodeURIComponent(q)}`)
						}}
					>
						<input type="search" placeholder="자산 검색 — 이름, 태그, 시리얼, 모델, 사용자" value={q} onChange={(e) => setQ(e.target.value)} />
					</form>
					<NavLink to="/scan" className="scan-btn" title="스캔">
						⌗ 스캔
					</NavLink>
					<Bell />
				</header>
				<Offline />
				<main className="content">{props.children}</main>
			</div>
		</div>
	)
}

export function App(props: { account: Account; onSignOut: () => void }): ReactNode {
	// Every route is inside the workspace's address, so a link written `/assets`
	// is `/@<alias>/assets`.
	return (
		<BrowserRouter basename={`/@${encodeURIComponent(workspace)}`}>
			<CatalogProvider>
				<Routes>
					<Route path="/labels/print" element={<LabelSheet />} />
					<Route
						path="*"
						element={
							<Frame account={props.account} onSignOut={props.onSignOut}>
								<Routes>
									<Route path="/" element={<Dashboard />} />
									<Route path="/assets" element={<Assets />} />
									<Route path="/assets/:id" element={<AssetPage />} />
									<Route path="/spaces" element={<Spaces />} />
									<Route path="/reservations" element={<Reservations />} />
									<Route path="/custody" element={<Custodies />} />
									<Route path="/custody/:id" element={<CustodyPage />} />
									<Route path="/stock" element={<Stocks />} />
									<Route path="/counts" element={<Counts />} />
									<Route path="/counts/:id" element={<CountPage />} />
									<Route path="/work" element={<Work />} />
									<Route path="/purchases" element={<Purchases />} />
									<Route path="/people" element={<People />} />
									<Route path="/settings" element={<Settings />} />
									<Route path="/scan" element={<Scan />} />
									<Route path="/scan/:code" element={<Scan />} />
									<Route path="/notifications" element={<Notifications />} />
									<Route path="/me" element={<Me />} />
									<Route path="*" element={<Navigate to="/" replace />} />
								</Routes>
							</Frame>
						}
					/>
				</Routes>
			</CatalogProvider>
		</BrowserRouter>
	)
}
