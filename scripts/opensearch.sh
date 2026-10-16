#!/usr/bin/env bash
# opensearch.sh -- an OpenSearch for CI to search on (D31): the image a
# deployment runs (deploy/opensearch), with its security plugin on over the
# demo certificates, and Rove signed in with the role README gives an operator
# -- bulk writes, and its own indexes -- so that a call needing more fails here
# and not at an operator's.
#
#   scripts/opensearch.sh start          # build and run it, in the background
#   scripts/opensearch.sh ready PATTERN  # wait for it, and make the role
#
# `start` returns at once: the image builds and the node comes up while a job
# does the rest of its setup. `ready` waits for it, makes the role on the
# index names PATTERN matches (`rovetest-*` for the Go tests, `e2e-*` for the
# browser's) and a user with it, and prints what the tests are given:
# PDTEST_OPENSEARCH and E2E_OPENSEARCH, the user in the address, and the CA in
# the matching _CA variable -- lines for $GITHUB_ENV.
set -euo pipefail

dir=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/opensearch
root=$(cd "$(dirname "$0")/.." && pwd)

case ${1:-} in
start)
	mkdir -p "$dir"
	# Letters of every kind, which the demo's own check asks of it.
	echo "Ad9!$(openssl rand -hex 16)" > "$dir/admin"
	chmod 600 "$dir/admin"
	(
		docker build -q -t rove-opensearch "$root/deploy/opensearch"
		docker run -d --name opensearch -p 127.0.0.1:9200:9200 \
			-e discovery.type=single-node -e OPENSEARCH_INITIAL_ADMIN_PASSWORD="$(cat "$dir/admin")" \
			-e OPENSEARCH_JAVA_OPTS="-Xms512m -Xmx512m" rove-opensearch
	) > "$dir/start.log" 2>&1 &
	echo "$!" > "$dir/start.pid"
	;;
ready)
	pattern=${2:?"usage: scripts/opensearch.sh ready PATTERN"}
	admin=$(cat "$dir/admin")
	for i in $(seq 1 150); do
		curl -skf -u "admin:$admin" https://localhost:9200/_cluster/health > /dev/null && break
		if [ "$i" -eq 150 ]; then
			echo "OpenSearch did not come up" >&2
			cat "$dir/start.log" >&2
			docker logs opensearch 2>&1 | tail -50 >&2 || true
			exit 1
		fi
		sleep 2
	done
	ca=$dir/ca.pem
	docker cp opensearch:/usr/share/opensearch/config/root-ca.pem "$ca" >&2
	api() {
		curl -sS --fail-with-body --cacert "$ca" -u "admin:$admin" -H 'Content-Type: application/json' \
			-X PUT "https://localhost:9200/_plugins/_security/api/$1" -d "$2" >&2
		echo >&2
	}
	# README's role, on the names the tests make their indexes with.
	api roles/rove "{\"cluster_permissions\":[\"indices:data/write/bulk\"],\"index_permissions\":[{\"index_patterns\":[\"$pattern\"],\"allowed_actions\":[\"indices_all\"]}]}"
	password="Rv9-$(openssl rand -hex 16)"
	api internalusers/rove "{\"password\":\"$password\"}"
	api rolesmapping/rove '{"users":["rove"]}'
	echo "PDTEST_OPENSEARCH=https://rove:$password@localhost:9200"
	echo "PDTEST_OPENSEARCH_CA=$ca"
	echo "E2E_OPENSEARCH=https://rove:$password@localhost:9200"
	echo "E2E_OPENSEARCH_CA=$ca"
	;;
*)
	echo "usage: scripts/opensearch.sh start | ready PATTERN" >&2
	exit 2
	;;
esac
