#!/bin/sh
# Stands up the levyan the billing pages are tried against (playwright.config.ts,
# billing.spec.ts): internal/levyantest -- what the Go tests bill to -- with its
# gRPC on 127.0.0.1:PORT, where the loft `serve.sh billing` starts reaches it,
# and on 127.0.0.1:PORT+1 what a test tells it to say of a workspace.
#
#   sh e2e/levyan.sh PORT
#
# E2E_LEVYAN names one already built, as E2E_LOFT names loft.
set -eu

port=${1:-}
case $port in
'' | *[!0-9]*)
	echo "usage: sh e2e/levyan.sh PORT" >&2
	exit 2
	;;
esac
here=$(cd "$(dirname "$0")" && pwd)
ts=$(dirname "$here")
root=$(dirname "$ts")

bin=${E2E_LEVYAN:-}
if [ -z "$bin" ]; then
	dir=$ts/test-results/levyan-$port
	mkdir -p "$dir"
	bin=$dir/levyantest
	(cd "$root" && go build -o "$bin" ./internal/levyantest/cmd/levyantest)
fi

exec "$bin" -addr "127.0.0.1:$port" -control "127.0.0.1:$((port + 1))"
