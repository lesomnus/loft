#!/bin/sh
# Stands up the rove the browser tests run against (playwright.config.ts):
# built from this checkout, `init --demo` on a SQLite database of its own, and
# serving the built page (ts/dist) on 127.0.0.1:PORT.
#
#   sh e2e/serve.sh PORT [consent | sso ISSUER]
#
# Its database, files and log are in test-results/rove-PORT, which Playwright
# empties before every run and leaves after one, for a failure to be read.
# `consent` also asks everybody to agree to terms first (`app.consent`), and
# `sso` signs people in through the issuer at ISSUER as well (`auth.sso`):
# the test's own, which e2e/issuer.sh stands up.
#
# E2E_POSTGRES names a PostgreSQL to run on instead -- postgres://…, as one
# who may make databases there. Each server then has two of its own, Rove's
# and the roster's in it, e2e_rove_PORT and e2e_roster_PORT, dropped and made
# again every run (e2e/postgres.mjs); the database E2E_POSTGRES names is only
# where they are made from.
#
# Nothing of a desk's reaches it: its configuration is the environment written
# here, every other ROVE_* is unset, and there is no rove.yaml where it runs.
# E2E_ROVE names a rove already built -- CI's, or one built on a host for a
# container that has no Go.
set -eu

port=${1:-}
case $port in
'' | *[!0-9]*)
	echo "usage: sh e2e/serve.sh PORT [consent | sso ISSUER]" >&2
	exit 2
	;;
esac
here=$(cd "$(dirname "$0")" && pwd)
ts=$(dirname "$here")
root=$(dirname "$ts")
dir=$ts/test-results/rove-$port
log=$dir/rove.log

if [ ! -f "$ts/dist/index.html" ]; then
	echo "serve.sh: the page is not built; npm run build" >&2
	exit 1
fi

rm -rf "$dir"
mkdir -p "$dir"

bin=${E2E_ROVE:-}
if [ -z "$bin" ]; then
	bin=$dir/rove
	(cd "$root" && go build -o "$bin" ./cmd/rove)
fi

for v in $(env | sed -n 's/^\(ROVE_[A-Za-z0-9_]*\)=.*/\1/p'); do
	unset "$v"
done
if [ -n "${E2E_POSTGRES:-}" ]; then
	dsns=$(node "$here/postgres.mjs" "$E2E_POSTGRES" "e2e_rove_$port" "e2e_roster_$port")
	export ROVE_DB_DRIVER=pgx
	ROVE_DB_DSN=$(echo "$dsns" | sed -n 1p)
	export ROVE_DB_DSN
	export ROVE_AUTH_ROSTER_DB_DRIVER=pgx
	ROVE_AUTH_ROSTER_DB_DSN=$(echo "$dsns" | sed -n 2p)
	export ROVE_AUTH_ROSTER_DB_DSN
	export ROVE_AUTH_ROSTER_DB_MIGRATE=true
else
	export ROVE_DB_DRIVER=sqlite3
	export ROVE_DB_DSN="file:$dir/rove.db?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)"
fi
export ROVE_DB_MIGRATE=true
export ROVE_WATCH_BROKER=memory
export ROVE_SPIN_LEASE=local
export ROVE_SERVER_ADDR=127.0.0.1:$((port + 1))
export ROVE_SERVER_HTTP_ADDR=127.0.0.1:$port
export ROVE_SERVER_HTTP_ALLOW_WEB=true
export ROVE_APP_PUBLIC_URL=http://127.0.0.1:$port
export ROVE_APP_WEB=$ts/dist
export ROVE_APP_FILES=$dir/files
export ROVE_APP_SIGNING_KEY=rove-e2e-signing-key-not-a-secret-$port
case ${2:-} in
consent)
	export ROVE_APP_CONSENT_TERMS_VERSION=2026-10-01
	export ROVE_APP_CONSENT_TERMS_URL=https://example.com/terms
	export ROVE_APP_CONSENT_PRIVACY_VERSION=2026-10-01
	export ROVE_APP_CONSENT_PRIVACY_URL=https://example.com/privacy
	;;
sso)
	# The client e2e/issuer.sh registers, with one button.
	export ROVE_AUTH_SSO_ISSUER=${3:?"usage: sh e2e/serve.sh PORT sso ISSUER"}
	export ROVE_AUTH_SSO_CLIENT_ID=rove-e2e
	export ROVE_AUTH_SSO_CLIENT_SECRET=rove-e2e-client-secret-not-a-secret
	export ROVE_AUTH_SSO_PROVIDERS='[{"connection":"google","label":"Google","mark":"google"}]'
	;;
esac

# It writes a line for every call, so what it says goes to the log, and only
# the end of that is shown -- when it stops on its own, which is a failure.
trap 'status=$?; [ "$status" -eq 0 ] || tail -n 40 "$log" >&2' EXIT

cd "$dir"
"$bin" init --tenant acme --login owner --password owner-pass-1234 --demo >"$log" 2>&1
"$bin" serve >>"$log" 2>&1
