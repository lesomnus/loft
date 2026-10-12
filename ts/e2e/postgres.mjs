// The databases one server of the browser tests runs on, made fresh: e2e/serve.sh
// runs this when E2E_POSTGRES names a PostgreSQL to make them on.
//
//   node e2e/postgres.mjs <E2E_POSTGRES> <name>...
//
// Each database named is dropped, whoever is still connected to it, and made
// again, so a run never starts on what the last one left; then its address is
// written out, a line each, in the order named.
//
// In node, because node is wherever the tests are -- a desk, CI, the Playwright
// image -- and psql and Go are not.
import pg from 'pg'

const [at, ...names] = process.argv.slice(2)
if (at === undefined || names.length === 0) {
	console.error('usage: node e2e/postgres.mjs <postgres://…> <name>...')
	process.exit(2)
}

const db = new pg.Client({ connectionString: at })
await db.connect()
try {
	for (const name of names) {
		// A name goes into the statement as it is, so it is only ever one of
		// the names serve.sh makes up.
		if (!/^e2e_[a-z0-9_]+$/.test(name)) throw new Error(`${name} is not a name the tests make`)
		await db.query(`DROP DATABASE IF EXISTS ${name} WITH (FORCE)`)
		await db.query(`CREATE DATABASE ${name}`)

		const u = new URL(at)
		u.pathname = `/${name}`
		console.log(u.toString())
	}
} finally {
	await db.end()
}
