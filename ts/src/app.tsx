/**
 * The frame every page is drawn in, and which page is where.
 *
 * @module
 */

import { useState, type ReactNode } from 'react'
import { BrowserRouter, NavLink, Navigate, Route, Routes, useNavigate } from 'react-router-dom'

import { AssetService, NotificationService, PurchaseService, type Session } from './api.js'
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
import { Register } from './pages/register.js'
import { Reports } from './pages/reports.js'
import { Reservations } from './pages/reservations.js'
import { Scan } from './pages/scan.js'
import { Settings } from './pages/settings.js'
import { Spaces } from './pages/spaces.js'
import { Stocks } from './pages/stock.js'
import { Work } from './pages/work.js'
import type { DescMethod } from '@bufbuild/protobuf'
import { Offline, useRpc } from './rpc.js'
import { BillingBanner } from './billing.js'

// `may` is what a page is for, which somebody roster says may not call it
// does not see in the menu (D89).
const nav: { to: string; label: string; icon: string; may?: DescMethod }[] = [
	{ to: '/', label: '대시보드', icon: '◎' },
	{ to: '/assets', label: '자산', icon: '▦' },
	{ to: '/spaces', label: '공간', icon: '⌂' },
	{ to: '/reservations', label: '예약', icon: '◷' },
	{ to: '/custody', label: '지급·대여', icon: '⇄' },
	{ to: '/stock', label: '재고', icon: '▤' },
	{ to: '/counts', label: '실사', icon: '✓' },
	{ to: '/work', label: '작업', icon: '⚒' },
	{ to: '/purchases', label: '구매', icon: '¤', may: PurchaseService.method.add },
	{ to: '/reports', label: '리포트', icon: '▥', may: AssetService.method.census },
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

function Frame(props: { session: Session; onSignOut: () => void; children: ReactNode }): ReactNode {
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
						<strong>Loft</strong>
					</div>
				</div>
				<div className="workspace-name">{c.me.workspaceName || props.session.workspace.name}</div>
				<nav>
					{nav
						.filter((n) => n.may === undefined || c.may(n.may))
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
				<BillingBanner session={props.session} />
				<main className="content">{props.children}</main>
			</div>
		</div>
	)
}

export function App(props: { session: Session; onSignOut: () => void }): ReactNode {
	// The page is the one workspace the deployment serves (D85): every route
	// is at the root.
	return (
		<BrowserRouter>
			<CatalogProvider>
				<Routes>
					<Route path="/labels/print" element={<LabelSheet />} />
					<Route
						path="*"
						element={
							<Frame session={props.session} onSignOut={props.onSignOut}>
								<Routes>
									<Route path="/" element={<Dashboard />} />
									<Route path="/assets" element={<Assets />} />
									<Route path="/assets/register" element={<Register />} />
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
									<Route path="/reports" element={<Reports />} />
									<Route path="/people" element={<People />} />
									<Route path="/settings" element={<Settings session={props.session} />} />
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
