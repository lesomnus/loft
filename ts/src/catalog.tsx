/**
 * Who is signed in, and the small tables every page names things from: the
 * types, the models, the people and teams, the spaces.
 *
 * They are read once and again after any write, which for a tenant of a few
 * hundred of each is cheaper than asking for a name every time one is drawn.
 *
 * @module
 */

import { createContext, useContext, useMemo, type ReactNode } from 'react'

import type { Asset } from '../gen/rove/asset_pb.js'
import type { Bookable } from '../gen/rove/booking_pb.js'
import type { AssetType, ItemModel } from '../gen/rove/catalog_pb.js'
import type { Party } from '../gen/rove/org_pb.js'
import type { PartyMeResponse } from '../gen/rove/org_svc_pb.js'
import { AssetService, AssetTypeService, BookableService, ItemModelService, PartyService, idStr } from './api.js'
import { useRpc } from './rpc.js'
import { Spinner } from './ui.js'

/** The levels the server's role table uses; see `server/policy`. */
const levels: Record<string, number> = { auditor: 1, member: 2, manager: 3, admin: 4, owner: 5 }

export interface Catalog {
	me: PartyMeResponse
	role: string
	/** Whether the signed-in person is at least `role`. */
	can: (role: 'member' | 'manager' | 'admin' | 'owner') => boolean

	/**
	 * How many days back the tenant's history goes for it, by its contract
	 * (design 8.1). Zero is all of it.
	 */
	historyDays: number
	/**
	 * The oldest moment worth asking the history about, an hour inside the
	 * window so that a page left open does not drift out of it; undefined is
	 * all of it.
	 */
	historyFrom: () => Date | undefined
	/** `d`, or the oldest moment worth asking about when `d` is older. */
	inHistory: (d: Date) => Date

	types: AssetType[]
	models: ItemModel[]
	parties: Party[]
	spaces: Asset[]
	bookables: Bookable[]

	type: (id: Uint8Array | undefined) => AssetType | undefined
	model: (id: Uint8Array | undefined) => ItemModel | undefined
	party: (id: Uint8Array | undefined) => Party | undefined
	space: (id: Uint8Array | undefined) => Asset | undefined
	bookable: (asset: Uint8Array | undefined) => Bookable | undefined
	/** A space's place in the tree, as names from the top. */
	path: (id: Uint8Array | undefined) => string
}

const Ctx = createContext<Catalog | undefined>(undefined)

export function useCatalog(): Catalog {
	const v = useContext(Ctx)
	if (v === undefined) throw new Error('no catalog')
	return v
}

function byId<T extends { id: Uint8Array }>(vs: T[]): Map<string, T> {
	return new Map(vs.map((v) => [idStr(v.id), v]))
}

export function CatalogProvider(props: { children: ReactNode }): ReactNode {
	const me = useRpc(PartyService.method.me, {})
	const types = useRpc(AssetTypeService.method.list, { size: 500 })
	const models = useRpc(ItemModelService.method.list, { size: 500 })
	const parties = useRpc(PartyService.method.list, { size: 500 })
	const spaces = useRpc(AssetService.method.search, { kind: 'space', size: 500 })
	const bookables = useRpc(BookableService.method.list, { size: 500 })

	const v = useMemo((): Catalog | undefined => {
		if (me.data === undefined) return undefined
		const t = byId(types.data?.items ?? [])
		const m = byId(models.data?.items ?? [])
		const p = byId(parties.data?.items ?? [])
		const s = byId(spaces.data?.items ?? [])
		const b = new Map((bookables.data?.items ?? []).map((v) => [idStr(v.asset?.id), v]))
		const role = me.data.role
		const path = (id: Uint8Array | undefined): string => {
			const names: string[] = []
			let at = s.get(idStr(id))
			for (let i = 0; at !== undefined && i < 32; i++) {
				names.unshift(at.name)
				at = s.get(idStr(at.parentId))
			}
			return names.join(' › ')
		}
		const days = me.data.historyDays
		const historyFrom = (): Date | undefined => (days > 0 ? new Date(Date.now() - days * 86400_000 + 3600_000) : undefined)
		return {
			me: me.data,
			role,
			can: (r) => (levels[role] ?? 0) >= (levels[r] ?? 9),
			historyDays: days,
			historyFrom,
			inHistory: (d) => {
				const from = historyFrom()
				return from !== undefined && d < from ? from : d
			},
			types: types.data?.items ?? [],
			models: models.data?.items ?? [],
			parties: parties.data?.items ?? [],
			spaces: spaces.data?.items ?? [],
			bookables: bookables.data?.items ?? [],
			type: (id) => t.get(idStr(id)),
			model: (id) => m.get(idStr(id)),
			party: (id) => p.get(idStr(id)),
			space: (id) => s.get(idStr(id)),
			bookable: (id) => b.get(idStr(id)),
			path,
		}
	}, [me.data, types.data, models.data, parties.data, spaces.data, bookables.data])

	// Every list once before anything is drawn, so that a page reading a
	// default out of them on its first render does not read an empty one.
	const ready = [types, models, parties, spaces, bookables].every((q) => q.data !== undefined || q.state === 'error')
	if (v === undefined || !ready) {
		return <div className="boot">{me.state === 'error' ? '불러오지 못했습니다.' : <Spinner />}</div>
	}
	return <Ctx.Provider value={v}>{props.children}</Ctx.Provider>
}

/** How far back the history goes, said where a page asks about the past; nothing when it is all of it. */
export function HistoryDays(): ReactNode {
	const c = useCatalog()
	if (c.historyDays === 0) return null
	return <> 이 조직은 최근 {c.historyDays}일의 이력을 볼 수 있습니다.</>
}

/** A model as people say it: maker and name. */
export function modelName(m: ItemModel | undefined): string {
	if (m === undefined) return ''
	return [m.maker, m.name].filter((v) => v !== '').join(' ')
}
