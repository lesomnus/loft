#!/bin/sh
# Stands up the rove the browser tests run against (playwright.config.ts):
# built from this checkout, `init --demo` on a SQLite database of its own, and
# serving the built page (ts/dist) on 127.0.0.1:PORT.
#
#   sh e2e/serve.sh PORT [consent]
#
# Its database, files and log are in test-results/rove-PORT, which Playwright
# empties before every run and leaves after one, for a failure to be read.
# `consent` also asks everybody to agree to terms first (`app.consent`).
#
# Nothing of a desk's reaches it: its configuration is the environment written
# here, every other ROVE_* is unset, and there is no rove.yaml where it runs.
# E2E_ROVE names a rove already built -- CI's, or one built on a host for a
# container that has no Go.
set -eu

port=${1:-}
case $port in
'' | *[!0-9]*)
	echo "usage: sh e2e/serve.sh PORT [consent]" >&2
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
export ROVE_DB_DRIVER=sqlite3
export ROVE_DB_DSN="file:$dir/rove.db?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)"
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
if [ "${2:-}" = consent ]; then
	export ROVE_APP_CONSENT_TERMS_VERSION=2026-10-01
	export ROVE_APP_CONSENT_TERMS_URL=https://example.com/terms
	export ROVE_APP_CONSENT_PRIVACY_VERSION=2026-10-01
	export ROVE_APP_CONSENT_PRIVACY_URL=https://example.com/privacy
fi

# It writes a line for every call, so what it says goes to the log, and only
# the end of that is shown -- when it stops on its own, which is a failure.
trap 'status=$?; [ "$status" -eq 0 ] || tail -n 40 "$log" >&2' EXIT

cd "$dir"
"$bin" init --tenant acme --login owner --password owner-pass-1234 --demo >"$log" 2>&1
"$bin" serve >>"$log" 2>&1
