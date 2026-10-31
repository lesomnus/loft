#!/bin/sh
# Stands up the issuer the sign-in through roster's is tried against
# (playwright.config.ts, sso.spec.ts): internal/oidctest -- what the Go tests
# sign in through -- served on 127.0.0.1:PORT, holding the loft at APP to what
# a relying party has to send.
#
#   sh e2e/issuer.sh PORT APP
#
# The client it knows is the one e2e/serve.sh configures with `sso`. E2E_ISSUER
# names one already built, as E2E_LOFT names loft.
set -eu

port=${1:-}
app=${2:-}
case $port in
'' | *[!0-9]*)
	echo "usage: sh e2e/issuer.sh PORT APP" >&2
	exit 2
	;;
esac
here=$(cd "$(dirname "$0")" && pwd)
ts=$(dirname "$here")
root=$(dirname "$ts")

bin=${E2E_ISSUER:-}
if [ -z "$bin" ]; then
	dir=$ts/test-results/issuer-$port
	mkdir -p "$dir"
	bin=$dir/oidctest
	(cd "$root" && go build -o "$bin" ./internal/oidctest/cmd/oidctest)
fi

exec "$bin" -addr "127.0.0.1:$port" -client loft-e2e -secret loft-e2e-client-secret-not-a-secret \
	-redirect "$app/sso/callback" -post-logout "$app/"
