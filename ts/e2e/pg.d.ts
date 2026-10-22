// What the browser tests ask of `pg`, which ships no types of its own: a
// client that runs a statement (domains.spec.ts; e2e/postgres.mjs is the
// other user, and plain JavaScript).
declare module 'pg' {
	export class Client {
		constructor(config: { connectionString: string })
		connect(): Promise<void>
		query(sql: string, values?: unknown[]): Promise<{ rowCount: number | null }>
		end(): Promise<void>
	}
	const pg: { Client: typeof Client }
	export default pg
}
