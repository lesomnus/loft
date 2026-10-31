#!/bin/sh
# Stands up the loft the browser tests run against (playwright.config.ts):
# built from this checkout, `init --demo` on a SQLite database of its own, and
# serving the built page (ts/dist) on 127.0.0.1:PORT.
#
#   sh e2e/serve.sh PORT [consent | sso ISSUER]
#
# Its database, files and log are in test-results/loft-PORT, which Playwright
# empties before every run and leaves after one, for a failure to be read.
# `consent` also asks everybody to agree to terms first (`app.consent`), and
# `sso` signs people in through the issuer at ISSUER as well (`auth.sso`):
# the test's own, which e2e/issuer.sh stands up.
#
# E2E_POSTGRES names a PostgreSQL to run on instead -- postgres://…, as one
# who may make databases there. Each server then has two of its own, Loft's
# and the roster's in it, e2e_loft_PORT and e2e_roster_PORT, dropped and made
# again every run (e2e/postgres.mjs); the database E2E_POSTGRES names is only
# where they are made from.
#
# E2E_OPENSEARCH names an OpenSearch to search on (D31) -- https://user:pass@…,
# the password in letters a URL leaves as they are, and its CA in
# E2E_OPENSEARCH_CA. Each server has indexes of its own there, e2e-PORT-…,
# taken out and made again from its database before it serves.
#
# Nothing of a desk's reaches it: its configuration is the environment written
# here, every other LOFT_* is unset, and there is no loft.yaml where it runs.
# E2E_LOFT names a loft already built -- CI's, or one built on a host for a
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
dir=$ts/test-results/loft-$port
log=$dir/loft.log

if [ ! -f "$ts/dist/index.html" ]; then
	echo "serve.sh: the page is not built; npm run build" >&2
	exit 1
fi

rm -rf "$dir"
mkdir -p "$dir"

bin=${E2E_LOFT:-}
if [ -z "$bin" ]; then
	bin=$dir/loft
	(cd "$root" && go build -o "$bin" ./cmd/loft)
fi

for v in $(env | sed -n 's/^\(LOFT_[A-Za-z0-9_]*\)=.*/\1/p'); do
	unset "$v"
done
if [ -n "${E2E_POSTGRES:-}" ]; then
	dsns=$(node "$here/postgres.mjs" "$E2E_POSTGRES" "e2e_loft_$port" "e2e_roster_$port")
	export LOFT_DB_DRIVER=pgx
	LOFT_DB_DSN=$(echo "$dsns" | sed -n 1p)
	export LOFT_DB_DSN
	export LOFT_AUTH_ROSTER_DB_DRIVER=pgx
	LOFT_AUTH_ROSTER_DB_DSN=$(echo "$dsns" | sed -n 2p)
	export LOFT_AUTH_ROSTER_DB_DSN
	export LOFT_AUTH_ROSTER_DB_MIGRATE=true
else
	export LOFT_DB_DRIVER=sqlite3
	export LOFT_DB_DSN="file:$dir/loft.db?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)"
fi
if [ -n "${E2E_OPENSEARCH:-}" ]; then
	scheme=${E2E_OPENSEARCH%%://*}
	node=${E2E_OPENSEARCH#*://}
	case $node in
	*@*)
		auth=${node%%@*}
		node=${node#*@}
		export LOFT_APP_SEARCH_OPENSEARCH_USERNAME="${auth%%:*}"
		export LOFT_APP_SEARCH_OPENSEARCH_PASSWORD="${auth#*:}"
		;;
	esac
	export LOFT_APP_SEARCH_OPENSEARCH_ADDRS="$scheme://$node"
	export LOFT_APP_SEARCH_OPENSEARCH_CA_FILE="${E2E_OPENSEARCH_CA:-}"
	export LOFT_APP_SEARCH_OPENSEARCH_INDEX="e2e-$port"
fi
export LOFT_DB_MIGRATE=true
export LOFT_WATCH_BROKER=memory
export LOFT_SPIN_LEASE=local
export LOFT_SERVER_ADDR=127.0.0.1:$((port + 1))
export LOFT_SERVER_HTTP_ADDR=127.0.0.1:$port
export LOFT_SERVER_HTTP_ALLOW_WEB=true
export LOFT_APP_PUBLIC_URL=http://127.0.0.1:$port
export LOFT_APP_WEB=$ts/dist
export LOFT_APP_FILES=$dir/files
export LOFT_APP_SIGNING_KEY=loft-e2e-signing-key-not-a-secret-$port
case ${2:-} in
consent)
	export LOFT_APP_CONSENT_TERMS_VERSION=2026-10-01
	export LOFT_APP_CONSENT_TERMS_URL=https://example.com/terms
	export LOFT_APP_CONSENT_PRIVACY_VERSION=2026-10-01
	export LOFT_APP_CONSENT_PRIVACY_URL=https://example.com/privacy
	;;
sso)
	# The client e2e/issuer.sh registers, with one button.
	export LOFT_AUTH_SSO_ISSUER=${3:?"usage: sh e2e/serve.sh PORT sso ISSUER"}
	export LOFT_AUTH_SSO_CLIENT_ID=loft-e2e
	export LOFT_AUTH_SSO_CLIENT_SECRET=loft-e2e-client-secret-not-a-secret
	export LOFT_AUTH_SSO_PROVIDERS='[{"connection":"google","label":"Google","mark":"google"}]'
	;;
esac

# It writes a line for every call, so what it says goes to the log, and only
# the end of that is shown -- when it stops on its own, which is a failure.
trap 'status=$?; [ "$status" -eq 0 ] || tail -n 40 "$log" >&2' EXIT

cd "$dir"
"$bin" init --tenant acme --login owner --password owner-pass-1234 --demo >"$log" 2>&1
if [ -n "${E2E_OPENSEARCH:-}" ]; then
	# What a run before left there is another database's.
	"$bin" search remove --yes >>"$log" 2>&1
	"$bin" search reindex >>"$log" 2>&1
fi
"$bin" serve >>"$log" 2>&1
