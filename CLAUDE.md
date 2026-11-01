# Working on loft

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
scripts/doctor.sh         # what CI runs: fails on what it says that is not known there
```

Doctor fails only what would stop a generation, and passes its advice;
`scripts/doctor.sh` keeps the advice read already, each by its first line
with what it is, and fails on the rest. Fix what it says, or add it there.

## Upgrading payday

```sh
go get github.com/lesomnus/roster@<the roster generated with that payday>
go tool pd upgrade --payday <version>   # go get, tidy, its floors, and its pd gen --ts
```

roster first: loft links the roster generated with the payday it links, and a
generated stamp from another payday is refused when it runs. `pd upgrade`
commits nothing. Renovate leaves payday, protobuf-orm and roster alone
(`renovate.json`, payday's `renovate/app` preset) -- only this moves them.

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

A rule about the row an `Add` writes goes on the server behind with
`bare.<E>WithAddRule` rather than in an `Add` you override: an entity that asks
for `AddMany` (`Fact` does) is written by both, and an override says it of one
(payday's `docs/guide/server.md`).

## Two servers, and one of them has no wall

`cmd/serve.go` builds `Walled` and `Ungated` (and `Base`, which is `Ungated`
without the domain layer). `Ungated` is not a privilege — it is an instance the
wall was never installed on, for work that cannot be done from inside a tenant
(`init`, resolving who is calling, the background sweeps).

**Never hand `Ungated` to anything a caller can reach.** There is no superuser
flag to check; the wiring is the whole of the control.

The one door that writes as the deployment on request is the operator's entry
point, `loft-admin serve` (`server/admin`, `cli/admin`): its own binary, on
`admin.addr`, behind operator keys. `loft` must never import either package --
`go list -deps ./cmd/loft` is how to check.

## The wall is a predicate, so it only applies to reads

`Add` has no row to narrow, so it is gated in the generated `Gate` layer
instead. If you are reasoning about who may create something, that is where it
is decided.

## Running

```sh
go run ./cmd/loft init --demo   # the first tenant, its owner, and five teams to try things on
go run ./cmd/loft serve         # :8080 serves the UI in ts/dist and the API
go run ./cmd/loft config env    # every variable this can be told through

cd ts && npm install && npm run build   # or `npm run dev` on :5173, proxied to :8080
```

`init` prints the owner's login; everybody `--demo` makes signs in with
`demo1234`. Delete `data/` to start over. PostgreSQL is
`LOFT_DB_DRIVER=pgx LOFT_DB_DSN=...`, and `cli.Migrate` then also installs
`cli/pg.sql` -- the constraints the schema cannot state. The roster in this
process then needs a database of its own named too:
`LOFT_AUTH_ROSTER_DB_DRIVER` and `LOFT_AUTH_ROSTER_DB_DSN`.

The browser tests are `ts/e2e` (`cd ts && npm run build && npx playwright
test`; README). Playwright starts a loft of its own -- `e2e/serve.sh`: this
checkout, `init --demo`, a database in `ts/test-results/`, or with
`E2E_POSTGRES` two made fresh on that PostgreSQL -- so they never touch
`data/`; and for the server that signs in through `auth.sso`, an issuer of
its own (`e2e/issuer.sh`: `internal/oidctest`, served by its `cmd/oidctest`).
CI runs them on both databases. A change to a page a flow goes through is not
done until they pass; find things by role and label, as a person does.

A form's `Field` (`ts/src/ui.tsx`) is a <label> beside the **one** control it
names, which takes the id the field hands it (`Labelled`). A field of more
than one control -- an asset picker, a row of boxes -- is `group`; never put
controls inside a <label> of your own.

## Signing in

People, their passwords and whether they may sign in are **roster's**
(`internal/identity`, design 9.10). `auth.roster.addr` names an external one;
empty runs one in this process on its own database (`roster.db` beside Loft's
SQLite file, or `auth.roster.db`), reached over `bufconn`.

**Two layers of tenant (D27).** A roster tenant is the **directory** accounts
are in -- one per deployment, `auth.roster.directory` (`loft` on the embedded
roster; the key's own tenant for an `rt_`) -- and a Loft tenant is a
**workspace**. A `Holder` is an account's membership of one workspace: an
identifier of its own, the roster account in `account`, and the `role`. Never
pass a workspace id where `identity.Store` wants an account, or the other way:
the store's API takes no tenant at all.

A browser signs in at `POST /session` (`server/session`): the password goes to
roster's `VouchService.Verify`, and the session -- a `global` row, written
through ent, served by no RPC -- names the **account**. Every call names its
workspace in `loft-workspace` (alias or id; `cmd.WorkspaceHeader`), or names
none when the account has one membership, and `cmd.Resolver` frames the call as
that membership. Every call is also held to what roster says of the account
now (`identity.Store.Held`, cached `HeldTtl`); a roster that cannot be asked
serves no session.

An external roster's sync stream (roster's `standing` package, roster#210:
`identity.Store.Accounts().Follower`, run from `cmd/serve.go` in every
process) is handed to `server/lifecycle`, so that roster's word lands
when it is said: signed out everywhere ends the sessions that began before it,
suspended or erased ends them all, and erased marks each of the account's
memberships `Holder.date_leaving`. roster's `restore` and `forget` write past
its servers and are **not on the stream** -- whatever acts on a mark asks
roster first. That is `Lifecycle.Sweep`: past `app.accounts.forget_after`, an
account roster still has nobody by leaves every workspace it is in
(`domain.Deps.Depart`: pseudonymized as 개인정보 삭제 is, and a last owner's
workspace handed on), and one roster has again stops leaving. `loft identity
depart` does it now for one account, and `Lifecycle.Reconcile` finds what the
stream missed -- it replays nothing -- every `app.accounts.reconcile` by
itself, and now with `loft identity reconcile`. The role Loft is installed with carries every method in
`identity.AgentMethods`; installing again leaves an existing role as it is, so
an older install is widened with `roster role patch` (README).

Through roster's issuer instead (`auth.sso`, `server/sso`), Loft is a relying
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

**Invitations are the one call served as an account** (D30). The account an
invitation is addressed to is nobody's member in that workspace yet, so
`InvitationService.Inbox`, `Claim`, `Accept` and `Deny` are marked
`(payday.method) = {account: {}}` (payday#184): `pd.AccountResolver` serves
them as the account itself (`frame.OfAccount`, no tenant), payday refuses such
a frame every other method, the policy gives them to the account alone
(`policy.Account`, never a membership, and never inside a membership's batch),
and each declares it reads nothing through the walled servers
(`pd.AccountReads`). `InvitationService.List` is the workspace's side, for its
administrators. `server/domain/invitation.go` says which rows each side
reads -- the account's own, by an address roster holds as checked
(`identity.Store.Addresses`) or a code its mail carried, and a workspace's own
for its administrators. An answer is written through `Deps.Base` as the
deployment's work in that workspace. Mail is `app.mail`, SMTP, and logged when
it names no server.

Roles stay Loft's. On the embedded roster the people screen and `loft holder
add|password` make accounts and set passwords there; on an external one they
are refused, and are done at roster. Ending a membership (deactivate,
pseudonymize) never touches the account. A deployment whose holders name no
account runs `loft identity migrate` once. `auth.Plain` is not wired; tests
mint a session with `Server.Sessions.Mint` and send its cookie (plus
`loft-workspace` when the account has more than one membership), and set
`c.Auth.Roster.Db` to a `pdtest.DB` of its own -- the default is a file.

## A workspace's end

A workspace is closed by its owner (`TenantService.Close`, typing its name) or
an operator (`loft tenant close`), or by the deployment when an account
leaving left nobody in it, and closing destroys nothing: `cmd.Resolver` refuses
every call into a closed workspace but its owner's `Get` and `Restore`, and the
owner can take its data out (`GET /account/workspaces/{alias}/export`). Once
its restore window is over -- its own `restore_days` (D36), or
`app.workspaces.restore_within` -- and nothing holds it, `server/closing`
destroys it and holds its address (`AliasHold`, whose reason says how it
went). Destroying retires it unless the contract in force then says
`purge_floors` (payday#115): what the deployment's floors (`audit.min`,
`audit.by.<kind>.min`) are on stays in the trail until each has passed, and
the rest goes now; a purge takes that too. `loft tenant retire|purge` are the
same destruction by an operator's hand (`closing.Destroy`), and a deployment
that bills measures the workspace before either takes anything, as well as
that can be done (Billing, below). With floors and no
archive a retirement is refused before anything is taken
(`offboard.ErrNoArchive`), and the sweep leaves the workspace closed. A
retired workspace imported back under its identifiers is first taken back
from the trail (`Policy.Return`, in `loft tenant import`), or the trail would
take what it writes. Addresses are made up (`tenancy.Aliases`) and nobody but
an operator moves one (`loft tenant alias`); the domain layer refuses an alias
in `TenantService.Patch`. A deployment that bills tells levyan each step --
closed, restored, destroyed (Billing, below) -- so a new way to close or
destroy a workspace goes through `Tx.closeWorkspace`, `restoreWorkspace` or
`closing.Destroy`, or levyan bills it as if it were open.

## Where a workspace is

A workspace says its own zone, currency and check-in rule (loft#144), and how
long it keeps its history and its access record and may be restored once
closed (`keep_days`, `access_keep_days`, `restore_days`, D36), its owner
through `TenantService.Configure` -- the one door, with its event; `Patch`
refuses them -- and the deployment's `app.locale` and `app.no_show_after`
answer while it says nothing. A window made shorter waits out
`app.retention.grace`, and `TenantService.Preview` says what a change takes,
and from when, before it is saved. What is kept is what is seen: there is no
view window. In the domain layer that is
`t.zone()`, `t.when(at)` and `t.settings()`: a date a person reads, a file's
dates, the day a count falls on, all in the workspace's zone, never a fixed
one. `PartyService.Me` answers what the server tells it in -- and `GET
/account`'s `timezone`, the deployment's `app.locale`, for the pages outside
any workspace (`/billing`, the inbox) -- and the page draws
and reads dates on that wall whatever the browser's zone (`localize`;
`fromInput` and never `new Date(v)` for an input, `wall`, `at` and `shift` for
a calendar's days). An amount is a whole number of its currency's minor unit
-- won for KRW, cents for USD (`internal/money`, and `money`/`amountIn` in
`ts/src/api.ts`) -- and a row keeps the currency it was priced in, so amounts
of two currencies are never added together.

## The terminal

Everything is an RPC, and `loft api` is every one of them as a command,
asked of a running deployment over the address a browser reaches it at
(`cli/connect.go` speaks Connect to the same transcoder the page does): payday's
entity tree (`pdcmd`), every other method of an entity's service beside it
(`loft api asset move`), and `loft api call` by name. A terminal signs in with
`loft login`, which a person signed in at the app approves (`server/clilogin`):
the terminal makes up its session key and sends only its hash, so the session
is a browser's session in every respect. These commands read no server
configuration; their own is `LOFT_CLIENT_CONFIG`. A new feature is an RPC, so it
is a command too -- and anything HTTP-only beside the RPCs (`/account`) gets a
command of its own (`loft workspace`).

A terminal may sign in narrowed (`loft login --allow PATTERN`, D33): the page
that approves it shows the methods, and the session is narrowed to them --
`Session.grant`, a payday `frame.Grant` that the gRPC chain holds every call
to, so whoever holds the key can do that much and no more. Beside the RPCs a
narrowed session only reads what its account is (`account.Reading`);
everything else asks `account.SignedIn`, which refuses it -- a narrowed
terminal that could approve another would have been narrowed to nothing.

`loft mcp` is that terminal as an MCP server on stdin and stdout (`cli/mcp.go`,
D32): Claude Code or Claude Desktop starts it, and every tool is RPCs as the
person signed in, read again each call, through the same policy and wall. The
deployment does not know there is an assistant. A change is two tools (D33,
`cli/mcp_write.go`, `cli/mcp_more.go`): a `plan_*` reads what it names and answers, in names, what
would happen and a token, changing nothing; `apply` takes the token and that
sentence word for word -- so the client's prompt before it shows what it does
-- and makes the call, with the op the plan minted. Who may is the policy
table's and the domain's, at apply; a narrowed sign-in is told at the plan. A
tool
answers with the names a person reads beside the ids other tools take, and
never a person's email or phone: what it answers goes to the assistant's
provider. A person's words -- a reason, a memo -- it answers as the page shows
them, read through the same RPCs, and so on the access record as the person's
read (D56). Its tests run the command over pipes with the SDK's client
(`cli/mcp_test.go`).

## Background work

What `cmd/serve.go` appends to `s.Spin` runs under `spin.lease` (payday#54):
every loop in **one** process of the deployment, but the ones marked
`spin.InEveryProcess` -- a process's own work, as the roster stream that drops
its cached answers is. So a loop has a name of its own (`spinEvery("loft.…",
…)`, `spin.Called`): `spin.Run` refuses an unnamed one before anything starts.
Hand over a `spin.Spinner`, never a thing with a `Run` method -- anything else
in the list is skipped without a word. A lease is at most one at a time but for
a moment around a failover, not exactly once: a loop is still safe run twice.

## Billing

What a workspace holds is measured by Loft, from its own tables, and handed to
levyan, the billing service (levyan#14, payday#186, design 7, D59); Loft
writes no rows for a bill. The meters are declared in `proto/loft/meter.proto`
-- `loft.assets.active`, `loft.attachments.bytes`, `loft.members` -- which
`pd gen` holds to the entities and turns into `pd.Meters`, and one a
declaration cannot say is Go beside them: `retention.Meter`,
`loft.history.retention_days`, the keep window in force as
`TenantService.Preview` reads it, 0 for forever. A key is chosen once, since it
is what levyan prices: a meter that comes to count something else is a new
key. `Asset.status` is a string, which `pd gen` holds no value of to anything,
so renaming `disposed` or `lost` is a meter that counts it again without a
word.

`cmd.Build` makes `s.Meters`, payday's `meter.Reader`, only when
`app.billing.levyan` names levyan, and appends it to `s.Spin`
(`payday.meter.read`, under the lease: at start and every midnight UTC); unset,
nothing is measured and nothing dials. `pd doctor` reads the wiring off the
syntax, so the literal stays where it is bound and spun. Its sink is
`server/levyan`, over levyan's own API module
(`levyan.v1.MeteringService/Record`, the product key as
`authorization: Bearer lvk_…`, a deadline on each call, TLS unless
`insecure`): a call that failed is an error, and the reader hands the same
readings over again; a reading levyan refused is logged and not resent. The
key is said nowhere. Tests bill to `internal/levyantest`.

Whatever destroys a workspace measures it first, as well as that can be done
and no more (Claude's rule, in place of the first one, that a workspace not
measured was not destroyed): a destruction is the law's and waits for no
billing service. `closing.Destroy`, which the sweep and `loft tenant
retire|purge` both go through, reads its meters (`Closing.Meters`) after the
holds are asked and before anything is taken, as the workspace's final
reading. One that cannot be made or handed over is a warning naming the
workspace and why, and the workspace destroyed all the same -- levyan bills
it by its last daily reading (its `usage.Hold`) -- and
`Destroyed.Unmeasured`, which the command says in a line. A dry run measures
nothing. A closed workspace is measured through its restore window like any
other; that levyan stops billing it at its closing is the lifecycle events'
to say: levyan bills none of the time it is closed, and nothing after it is
destroyed, as it is told (below). Flows (bytes sent and the like) are not
built yet.

What levyan grants each workspace is copied into this database (payday#185,
design 7.5, D60): `entitlements: {domain: 50}` in `proto/loft/meter.proto`,
kept by payday's `entitle.Follower` from levyan's `payday.GrantService` over
the same connection (`levyan.Client.Grants`, keepalive), under the lease
(`payday.entitle.follow`), built in `cmd.Build` beside the reader and only
when `app.billing.levyan` names levyan. The one key read is
`loft.restricted`: **on restricts, and no grant restricts nothing** -- a gap in
levyan never locks a workspace (levyan's design 13). A restricted workspace
may not grow what it is billed for; what that is, is one table,
`domain.Grows` -- methods, refused as an operation begins (`Tx.begin`, after
`past.Begin`, so a copy of an operation that happened answers as it did),
and cases the domain layer sees grow inside a method that does more
(`domain.GrowsAssetBack`, `GrowsKeptLonger`, `GrowsReceivedAssets`) -- read with
`entitle.Has` on the write's driver (`domain.Deps.Entitlements`, nil when
nothing bills) and refused `FailedPrecondition` with an
`errdetails.ErrorInfo` of reason `RESTRICTED`. That is the owner's first
stage of two (levyan#1's question 3, answered 2026-11-01: after the grace,
the day's work goes on and nothing grows); what growing is, that table, is
Claude's. A method gRPC does not name -- an operator's command, a sweep -- is
not refused: `loft holder add` included, which is the operator's door and not
a workspace's. A new RPC that adds to a meter goes in the table, or it is a
way around the restriction.

The second stage is `loft.readonly` (design 7.10, D64): sixty days after an
invoice billing the workspace first failed to be charged, or after it last
had a payer, levyan makes it read-only until it is paid for. Its table is
the other way round: what a read-only workspace **still does**,
`domain.ReadOnlyAllows` -- what levyan's `docs/apps.md` (3, "제한의 두 단계")
says an app takes of one: reading, taking its data out, paying, closing, its
members leaving; and a password, which is the account's -- each with why,
and every other write is refused. Keep it matching that chapter. Refused as
its operation begins (`Tx.begin`, `Tx.held`: read-only first, then
`Grows`), and, for a write that begins none, as the domain layer's
transaction a caller's method opened would commit (`Deps.run`); the
generated Erases a role is given are served in one (`domain.passing`).
`FailedPrecondition` with an `ErrorInfo` of reason `READONLY`, and
`PartyService.Me`'s `readonly` is what the banner reads, with levyan's
`readonly_from` (`BillingPayer`, `BillingDunning`) for when, to whoever reads
who pays. **A new write RPC is refused by a read-only workspace until that
table says why it is not, and a new read goes in it as `domain.Reads`**:
`server/domain/readonly_test.go` walks `server/policy`'s table and fails a
method that is neither in it nor served by this layer in a transaction of
its own, read off the layer's source. The doors that are no RPC -- a
download, the workspace's export, signing in -- read or are an account's,
and are not refused.

Who pays for a workspace is levyan's, and what it cannot know Loft tells it
(design 7.6): `levyan.v1.WorkspaceService` `Arrive` (made, by whom, when) and
`Leave` (an account no longer in it), written as a `LevyanNotice` (domain
51, global, `trail: off`) in the transaction of the change -- by
`tenancy.Workspaces`' making (sign-in, `POST /account/workspaces`), `loft
init`, `loft tenant import`, and by `levyan.Notices.Recorder` on every
`HolderService.Erase` through the servers -- and told by `levyan.Teller`
(`loft.levyan.notices`, under the lease), a workspace's in the order they
happened, a levyan that is down holding nothing up. A notice told is kept,
marked `date_told`. The loop's first pass in a process tells levyan of every
workspace it never told arrived, by its first owner (`tenancy.FirstOwners`,
D57's). roster's erase mark (`Holder.date_leaving`) tells nothing: the
account is told left when it leaves (`Deps.Depart`), since a mark is acted on
only once roster is asked again, and a `Leave` levyan heard is not undone by
roster restoring the account. The free-workspace rule is not built yet;
`ErrOwnsEnough` stays the deployment's cap until collection runs.

How a workspace is paid for is levyan's, and Loft asks and shows, storing
nothing of it (design 7.9, D63): `loft.PaymentService` (`proto/loft/payment.proto`,
`server/payment`), the one service that is no entity's, registered beside
the stack. A payer's seventeen methods are the account's own (`(payday.method)
= {account: {}}`, policy `Account`), each declaring it reads nothing
(`pd.AccountReads` with an empty `pd.Reads`, naming its constant -- the doctor
reads the call) and relayed to levyan's `PayerService` as the account
(`levyan.Client.As`: the product key and `levyan-account`). The workspace's
four -- `GetPayer`, `GetUsage`, `ListStatements` (admin), `Offer` (owner) --
ask about the caller's workspace alone, as the product. The messages are
levyan's API's copied number for number (`Billing*`), moved by marshalling,
and `server/payment`'s test holds every field to levyan's: bump
`github.com/lesomnus/levyan/api`, run it, and mirror what moved. A checkout
is opened on the checkout page, never in the app's document (whose policy
would have to let Paddle in everywhere). The page's account side is
`/billing` (`ts/src/billing.tsx`), a workspace's is settings › 결제, and the
banner reads `PartyService.Me`'s `restricted` and `readonly` -- the copy -- for every member,
`GetPayer` (payerless, `dunning`) for whoever runs it, and for a member who
pays the account's own calls (`ListLinks`, `GetBillingAccount`). The browser
tests bill to `internal/levyantest` served by its command
(`ts/e2e/levyan.sh`), told what to say on its control address.

What levyan's operator gives a workspace or a billing account -- an
allowance, a pool, credit (levyan#43, design 7.11, D65) -- both halves show
and nothing here grants: `GetUsage`'s allowances and pools and each line's
`allowance` and `pooled`, `GetPayer`'s credit, and the payer's
`GetBillingAccount` credit and balance and `GetInvoice` credits. A quantity
is levyan's decimal, exact to six digits: read exactly, and shown to one
digit after the point. An invoice's every line shows its statement, read as
the payer (`ListPayerStatements`, `GetPayerStatement`: levyan's
`PayerService.ListStatements` and `GetStatement`, levyan#47, D66) and never
with the product's key, which would show a payer what levyan gives it not;
the workspace's own (`ListStatements`) are its administrators', under
settings › 결제.

Paddle's default payment link is `/billing/checkout` (`server/checkout`,
design 7.7, D61): with `app.billing.checkout` -- Paddle's environment and the
client-side token, both public by Paddle's design -- it loads Paddle.js, which
opens the checkout of the transaction in `_ptxn` by itself; unset, it answers
404. Nobody signs in to it, and it reads and writes nothing. It is the one page
whose Content Security Policy names Paddle (`csp.With`, chosen by path in
`cmd.Front`): a host Paddle needs goes in `checkout.Sources` and nowhere else,
and only one Paddle.js is seen to load -- Paddle publishes no list. A token is
refused where it is written (`checkout.Token.Validate`; the block's
`Validate`, which the loader asks first, hands it the environment). The
browser tests replace Paddle.js (`ts/e2e/paddle.ts`), and nothing reaches
Paddle.

What became of a workspace is told the same way (design 7.8, D62): `close`
and `restore`, written by `Tx.closeWorkspace` and `restoreWorkspace` in their
transaction (`domain.Deps.Closings`) whoever closes it -- its owner, an
operator, the deployment when nobody is left -- of the moment it happened;
and `destroy`, written by `closing.Destroy` once its final reading is handed
over, whatever became of that, and before anything is taken: at a moment
after the reading, which its `AliasHold` is held from, once of a workspace. A
dry run writes nothing. A notice of these levyan answers it never heard of the
workspace for (NotFound) is told after the workspace's `Arrive`
(`Teller.unheard`): one waiting, the one levyan was told before again, or by
its first owner (origin `notfound`); one gone that levyan never heard arrive,
or one no account owns, is marked told with why. levyan refusing one is a
fault of Loft's, logged and not told again. The first pass also writes, once,
the `workspace.close` and `workspace.restore` events still kept (the mark
`past`), and as every pass starts, every workspace closed now and every one
destroyed that levyan was told arrived -- not one here again, which is
levyan's open question.

## What may be called

`server/policy` is one table of role → RPC, and what is not in it is refused --
which is also what keeps callers off the history rows only the domain layer
writes. A new RPC is closed until it is added there.

## The domain layer

`server/domain` sits **above** the gate, completes the generated verbs
(`Asset.Add`, `Reservation.Add`, ...) and adds the rest. An operation is one
transaction (`Domain.tx`), records an `Event`, and writes the time rows with the
state they describe.

Events and an asset's facts are **payday's past** (payday#151): its `past`
package writes them, and nothing else may -- the Audit layer refuses an
Event or a Fact written by hand, through every stack. `Tx.begin` is
`past.Begin` with the operation's kind and its kind's message
(`proto/loft/past.proto`), a fact is `t.op.Set` (or `addFact`, by the keys
Loft says them under), and what an asset was at a moment is
`past.StateAt` (`Tx.factsAt`).

**A kind has a message of its own**, named for what happened (`asset.move`
an `AssetMoved`), **unless it holds substantially what another kind of its
domain holds**: the same fields, or one's and a field or two more when the
two are one change -- a variant of it (`stock.receive`, `.consume` and
`.adjust` hold one `StockChanged`) or the thing written again (`type.add`
and `.update` one `AssetTypeSaved`). Then they share a message named for
the change they share, which lists beside it the fields each kind uses; a
kind leaves the rest empty. A kind that undoes another, or is another change
holding much the same, keeps its own. **Every field is its domain's**: the
head of `past.proto` has each domain's table, and a message takes its
fields from it under the same name and number, of the same type, with the
same meaning -- a new field takes the table's next number and goes in the
table. `pd gen` refuses a name with two numbers in a domain, and
`server/domain/uses_test.go` holds every message to its table, every table
field to a message, and every event a test records to what its kind uses.

An event keeps **no sentence**: it is said when it is read
(`server/domain/say.go`, `Sentences`, a sentence for each kind with
`past.SayKind`), in the workspace's zone, its people named as they are
called then (`who`, by identifier) -- so a message holds a person by
identifier only, and never anything of theirs. A new kind is a message (or
a kind of a shared one) and a sentence in `say.go`
(`TestEveryKindIsSaidInKorean`); it, a field, or a message **ships one
release before anything writes it**, so that the build before can read its
rows. `proto/loft/payday/past.lock` only grows: a message, a field, a JSON
name or a kind removed or changed, or a kind moved to another message, is a
row that no longer reads, and `pd gen` refuses it. `reason` is a person's
words, and personal; what Loft says itself goes in the message and its
sentence. A row from before keeps `desc` and `payload`, and is said by them
(`namer.say`, design 8.2).
A person forgotten (`Tx.forget`) has the trail's
rows blanked in the operation's transaction and the archive's half owed there
(payday#133); the operation that began the transaction pays it after its
commit (`Deps.Pay`), the trail's next pass pays what that could not, and `loft
trail owed` says what is left. Read `server/domain/domain.go` first. Its tests
(`go test ./server/domain`) go through the whole stack; set `PDTEST_POSTGRES` to
run them on PostgreSQL.

**An event's reason is taken out** when a person's request to erase what it
says of them points at it (`past.Redact`, `server/domain/redact.go`): the act
is an `event.redact` whose reason is the request's reference, the trail's
`payday.past.redact`, and the access record of what it read. Not out of the
event named alone: out of every event of its operation that says the same --
a part copies its operation's reason, and the act has a child for each (D49,
`domain.Copies`) -- out of every notice told in those words
(`reservation.decided`; D51, `domain.ToldWhy`, `domain.Told`), and out of
every row that keeps them as its own (a hand-over's memo, a scan's note, a
review's findings, why a workspace was closed; D56, `domain.KeptWhy`,
`domain.Kept`), patched through the servers so that the trail and the access
record say the act wrote it. All are found by the database comparing the
words where they are kept, never by reading them; mail already sent stays
said. A notice whose words are somebody's is of a kind in `ToldWhy`, and a
row's field that keeps a reason is in `KeptWhy` and declared personal, or
every domain test that writes one fails at its end. No role is given `EventService.Redact` yet (`closed` in
`server/policy`: the owner decides who); the operator does it with `loft
event redact`, on the workspace's word.

**A write through ent is not on the trail**, and `loft trail rows` then finds
the row -- a write past the servers is what it is there to find (payday#108).
Write through the servers. Where a write cannot be one -- a claim on a
condition, a restore -- put it on the trail as the act it is, in the same
transaction: `pd.Record` with `Deps.Audit`, as the mailer and `tenant import`
do. A kind no server writes says in its schema how its writes reach the trail
(`payday.entity`'s `trail`): `recorded`, and each write is recorded -- a
terminal's sign-in, an alias hold -- or `off: {why: ...}`, and it is not
checked -- a session, the tree lock. `pd gen` refuses one that says neither.
Whether a kind has RPCs is a separate question, about its API. An UPDATE that
changes nothing, to take a row's lock, is fine.

## Who saw whose data

Every call that carries somebody's personal data is a row of payday's access
record (loft#136, payday#111), read off what the schema declares `personal`:
a new RPC is recorded without a line. A person's own words are personal as a
reason is -- a memo, a note, what a review found -- and what a thing or a
document is called is not (design 8.2, D56): a field of free text a person
writes says which, and `pd doctor` asks of each on a kind that holds people. What no declaration sees -- a name
`namer.say` reads into a sentence, a column of a CSV, a name put in a mail --
says `access.Saw` in the call that shows it. A download, which hands people's
data out to be kept elsewhere, is in `cmd.Downloads` (the workspace's export is
HTTP, `server/account`, and records itself): it says what it is for in
`payday-reason-bin` -- base64, as every `-bin` key is -- or is refused. A file
that names nobody is no download, and is a method of its own
(`AssetService.Form`).

The workspace's administrators and auditors review it a month at a time
(settings › 접속기록): `AccessService.Review` reads a stretch out -- each
row's address and reason unmarshalled, the people it names by their names now,
which is itself on the record -- and an `AccessReview`, written by the domain's
`Add` and never changed, says who looked over which stretch and what it held.
An operator reads the deployment's with `loft access ls|reviews`, ids only.

## Search

Search is OpenSearch with nori, for a deployment whose
`app.search.opensearch` names a cluster (D31, loft#83); one that names none
asks the database (`internal/search`), which reads the tenant's rows with no
index -- pg_bigm is gone, and the database's search is not made faster. The
engine is `server/opensearch`: the client (its HTTP API, no library), and an
`Index` for each kind of thing searched -- `Assets`, `Parties`, `Models`
(loft#91), each with its own shape (`assets.json`, `parties.json`,
`models.json`, and its `Version`: bump it with any change), its own alias
`<index>-<name>` and `Reindex`, which fills a new index and moves the alias
onto it in one step; and `loft search status|reindex|remove|analyze`, which
go over all of them. The indexes are kept current by `opensearch.Recorder`,
which writes a `SearchStale` row in the transaction of any write to an asset,
a party or a model or to what their documents take a name from (type, model,
party, a party's team), and `opensearch.Indexer`, the loop that makes those
documents again from the database. A purge goes past the recorders, so it
writes its own row (`offboard.Deployment.Unindex`). The loop also makes an
index when there is none, or none of this build's `Version`. A call's own
writes are in the index before it answers (`Indexer.Interceptor`, in the gRPC
chain): save and search finds it; anything else is the loop's, a second or so
later. `AssetService.Search`, `PartyService.Search` and
`ItemModelService.Search` with text ask it (`Deps.Finder`), and read what it
found through the wall, in its order; a search with filters only, or a
deployment with no cluster, is the database's. The index is a copy: it is
never asked who may see what, and holds no person's email or phone.

A test that needs a cluster takes `internal/opensearchtest.Config(t)`
(`PDTEST_OPENSEARCH`, and `PDTEST_OPENSEARCH_CA` for TLS) and skips without
one, and makes every index with `opensearchtest.Made`. CI's `opensearch` job runs every such package on `deploy/opensearch`
with the security plugin on, signed in with the role README gives an
operator -- a call that needs more than that role fails there.

## Reference

- `README.md` — running it, in Korean
- `design.md`, `plan.md` — the design and the plan; `report.md` — what the
  prototype decided and why
- <https://github.com/lesomnus/payday/tree/main/docs> — the guides and the
  references behind them
