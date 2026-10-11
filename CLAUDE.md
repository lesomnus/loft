# Working on rove

A [payday](https://github.com/lesomnus/payday) app. Most of it is **generated
from `proto/`**, so the usual shape of a change is: edit the schema, regenerate,
then write the part no schema can state.

`README.md` is how to run it, `design.md` why it is shaped this way, and
`report.md` the decisions the prototype made.

## Regenerate after touching the schema

```sh
go tool pd gen .          # messages, ent schema, servers, layers
go tool pd gen --ts .     # and the TypeScript half
go tool pd gen --check .  # what CI runs: fails if anything moved
```

A generated file that was not regenerated **compiles perfectly and is wrong**.
If you edited anything under `proto/`, you are not done until `pd gen --check`
exits 0.

```sh
go tool pd doctor .       # what would go wrong, before it does
```

## Do not edit — regenerate

| | |
| --- | --- |
| `*.g.go`, `*.pb.go` | wherever `go_package` puts them |
| `server/bare/`, `server/pd/`, `internal/ent/` | in whole |
| `proto/payday/` | in whole — payday's entities, **copied** in |
| `proto/**/*_svc.g.proto` | the generated contract of an entity |
| `ts/gen/` | in whole |

**`.g` means a generator wrote it.** Everything else is yours — including
`proto/app/*.proto`, `proto/ext/**` (overlays), `cmd/`, and `ts/src/`.

To add a field to one of payday's entities, write an **overlay** in
`proto/ext/payday/`. Editing `proto/payday/` directly is undone by the next
generation.

## Adding an entity

```sh
go tool pd entity add --tenanted --watch Widget .
```

Use this rather than writing the file. It picks a free domain number and writes
the tenancy out — the two things that are cheap to get wrong and expensive to
find later.

Field numbers are read by name across every entity:

- **1** key · **2** tenant · **3** *yours, a set smaller than a tenant* ·
  **4** `alias` · **5** `name` · **6** `desc` · **7** `labels` ·
  **13/14/15** the timestamps
- **8–12 and 16+** are yours. An entity that does not want 4–7 **leaves those
  numbers empty** rather than spending them on something else.

## Writing a layer

A layer embeds `Overlay` — and must also write `WithDriver`, which nothing
inherits:

```go
func (s Core) WithDriver(drv dialect.Driver) (api.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return New(next), nil
}

var (
	_ api.Server               = Core{}
	_ enttx.Binder[api.Server] = Core{}
)
```

Leave it out and **nothing fails until a transaction is opened** — a batch, or a
multi-write RPC — because `enttx.Rebind` asks at run time. `pd doctor` finds it.

Note it is `enttx.Rebind(s.Next(), ...)` and not `s.Next().WithDriver(...)`:
`Next()` answers the generated `Server` interface, which deliberately has no
such method.

**Do not edit the generated `Gate`.** Your authorization goes in your own layer
in front of it, or into the `gate.Policy` you inject.

## Two servers, and one of them has no wall

`cmd/serve.go` builds `Walled` and `Ungated` (and `Base`, which is `Ungated`
without the domain layer). `Ungated` is not a privilege — it is an instance the
wall was never installed on, for work that cannot be done from inside a tenant
(`init`, resolving who is calling, the background sweeps).

**Never hand `Ungated` to anything a caller can reach.** There is no superuser
flag to check; the wiring is the whole of the control.

## The wall is a predicate, so it only applies to reads

`Add` has no row to narrow, so it is gated in the generated `Gate` layer
instead. If you are reasoning about who may create something, that is where it
is decided.

## Running

```sh
go run ./cmd/rove init --demo   # the first tenant, its owner, and five teams to try things on
go run ./cmd/rove serve         # :8080 serves the UI in ts/dist and the API
go run ./cmd/rove config env    # every variable this can be told through

cd ts && npm install && npm run build   # or `npm run dev` on :5173, proxied to :8080
```

`init` prints the owner's login; everybody `--demo` makes signs in with
`demo1234`. Delete `data/` to start over. PostgreSQL is
`ROVE_DB_DRIVER=pgx ROVE_DB_DSN=...`, and `cli.Migrate` then also installs
`cli/pg.sql` -- the constraints the schema cannot state. The roster in this
process then needs a database of its own named too:
`ROVE_AUTH_ROSTER_DB_DRIVER` and `ROVE_AUTH_ROSTER_DB_DSN`.

## Signing in

People, their passwords and whether they may sign in are **roster's**
(`internal/identity`, design 9.10). `auth.roster.addr` names an external one;
empty runs one in this process on its own database (`roster.db` beside Rove's
SQLite file, or `auth.roster.db`), reached over `bufconn`.

**Two layers of tenant (D27).** A roster tenant is the **directory** accounts
are in -- one per deployment, `auth.roster.directory` (`rove` on the embedded
roster; the key's own tenant for an `rt_`) -- and a Rove tenant is a
**workspace**. A `Holder` is an account's membership of one workspace: an
identifier of its own, the roster account in `account`, and the `role`. Never
pass a workspace id where `identity.Store` wants an account, or the other way:
the store's API takes no tenant at all.

A browser signs in at `POST /session` (`server/session`): the password goes to
roster's `VouchService.Verify`, and the session -- a `global` row, written
through ent, served by no RPC -- names the **account**. Every call names its
workspace in `rove-workspace` (alias or id; `cmd.WorkspaceHeader`), or names
none when the account has one membership, and `cmd.Resolver` frames the call as
that membership. Every call is also held to what roster says of the account
now (`identity.Store.Held`, cached `HeldTtl`); a roster that cannot be asked
serves no session.

Through roster's issuer instead (`auth.sso`, `server/sso`), Rove is a relying
party in the `itself` shape of roster's `docs/relying-party.md`: `GET
/sso/login?with=<connection>` sends the browser to Hydra naming the button's
connection, `GET /sso/callback` exchanges the code (state in a cookie, nonce,
PKCE, the secret in the header) and mints the same account session, keeping
the ID token beside it (`Session.held`) for `POST /sso/logout` to hand back as
`id_token_hint`. `GET /sso` is what the sign-in page draws. `auth.sso_only`
answers `POST /session` with 403. Tests sign in through `internal/oidctest`, an
issuer that holds a relying party to what it has to send.

An account in no workspace is given one of its own when it signs in
(`tenancy.Workspaces.Arrive`), and makes more at `POST /account/workspaces`;
`GET /account` is what it is in. Those are HTTP beside `/session` and not RPCs
(`server/account`), because every RPC is served as a membership of a
workspace and these are asked before there is one.

Roles stay Rove's. On the embedded roster the people screen and `rove holder
add|password` make accounts and set passwords there; on an external one they
are refused, and are done at roster. Ending a membership (deactivate,
pseudonymize) never touches the account. A deployment whose holders name no
account runs `rove identity migrate` once. `auth.Plain` is not wired; tests
mint a session with `Server.Sessions.Mint` and send its cookie (plus
`rove-workspace` when the account has more than one membership), and set
`c.Auth.Roster.Db` to a `pdtest.DB` of its own -- the default is a file.

## What may be called

`server/policy` is one table of role → RPC, and what is not in it is refused --
which is also what keeps callers off the history rows only the domain layer
writes. A new RPC is closed until it is added there.

## The domain layer

`server/domain` sits **above** the gate, completes the generated verbs
(`Asset.Add`, `Reservation.Add`, ...) and adds the rest. An operation is one
transaction (`Domain.tx`), records an `Event`, and writes the time rows with the
state they describe. Read `server/domain/domain.go` first. Its tests
(`go test ./server/domain`) go through the whole stack; set `PDTEST_POSTGRES` to
run them on PostgreSQL.

## Reference

- `README.md` — running it, in Korean
- `design.md`, `plan.md` — the design and the plan; `report.md` — what the
  prototype decided and why
- <https://github.com/lesomnus/payday/tree/main/docs> — the guides and the
  references behind them
