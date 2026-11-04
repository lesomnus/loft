#!/bin/sh
# A deployment with data in it, brought up to this build (#144): BEFORE, the
# build this one comes after, makes a database with a workspace, its demo and
# a contract in it, and NOW migrates it -- on SQLite, and on PostgreSQL when
# UPGRADE_POSTGRES names a server with the databases upgrade_loft and
# upgrade_roster on it, empty.
#
# Every other test starts from an empty database, which takes any column the
# schema adds; only one with rows in it says whether the column can be added
# to it. A column with no default cannot be, and the migration stops there.
#
# And what was made is still what the deployment says it is, once migrated:
# every row is what the trail's digest of it says (`trail rows`) -- so a
# migration that rewrites rows beneath the servers has put each on the trail
# as it went -- its windows are read (`retention plan`), and a workspace's
# export is taken and checked as this deployment's (`tenant export`, `tenant
# verify`). Both builds are given the keys a deployment's processes have: the
# one the trail's rows are digested with, and the one the trail is signed
# with, which signs an export too.
#
# What an operator does is asked of the entry point (D74): NOW's
# `loft-admin serve`, on a port of its own and with an operator's key only
# this holds, asked by its `loft` on an operator's connection (design 9.16).
# A BEFORE is one of three shapes: from before the entry point, it still has
# its operator's commands on the box (`loft contract set`), and is asked
# there; from before `loft` asked the entry point, its `loft-admin` asks it
# (`contract set`); and one after that is asked as NOW is (`tenant-contract
# add`).
#
# A migration says what it rewrote, as the move of an asset's facts into
# payday's past does (payday#151), and what it put on the trail of it: the
# same rows, every one. And what it held of what is out, which a build from
# before a hand-over held its things' time left holding none of it (D53):
# through the servers, so that `trail rows` finds each row as it was written.
# A second has nothing to rewrite, and nothing to hold.
#
#   scripts/upgrade.sh BEFORE NOW
#
# Each is a directory with a build's two binaries in it, `loft` and
# `loft-admin`. The entry point listens on 127.0.0.1:UPGRADE_ADMIN_PORT,
# 18081 when that says none.
set -eu

before=${1:?usage: scripts/upgrade.sh BEFORE NOW}
now=${2:?usage: scripts/upgrade.sh BEFORE NOW}
for b in "$before" "$now"; do
	for bin in loft loft-admin; do
		if [ ! -x "$b/$bin" ]; then
			echo "upgrade: $b has no $bin: BEFORE and NOW are each a directory with a build's loft and loft-admin" >&2
			exit 2
		fi
	done
done
dir=$(mktemp -d)
echo "upgrade: the databases and their logs are in $dir"
addr=127.0.0.1:${UPGRADE_ADMIN_PORT:-18081}
openssl rand -hex 32 >"$dir/admin.key"

for v in $(env | sed -n 's/^\(LOFT_[A-Za-z0-9_]*\)=.*/\1/p'); do
	unset "$v"
done
export LOFT_DB_MIGRATE=true
export LOFT_WATCH_BROKER=memory
export LOFT_SPIN_LEASE=local
export LOFT_APP_SIGNING_KEY=loft-upgrade-signing-key-not-a-secret
export LOFT_APP_FILES="$dir/files"
openssl rand -base64 32 >"$dir/digest.key"
openssl genpkey -algorithm ed25519 -out "$dir/checkpoints.pem"
export LOFT_AUDIT_DIGEST_KEY="$dir/digest.key"
export LOFT_AUDIT_CHECKPOINTS_KEY="$dir/checkpoints.pem"

# Both are told through LOFT_*. A build from before the rename from rove
# (2026-10-31) reads ROVE_* instead, so it would run on its own defaults --
# a database beside wherever this was started -- and nothing it makes is this
# build's to migrate: the rename broke compatibility once, on purpose, and CI
# does not run one (.github/workflows/ci.yaml, `upgrade`).
for b in "$before" "$now"; do
	if ! "$b/loft" config env 2>/dev/null | grep -qx LOFT_DB_DSN; then
		echo "upgrade: $b/loft is not told through LOFT_*: a build from before the rename from rove, whose data this build does not migrate" >&2
		exit 2
	fi
done

# run LOG BIN ARGS...: BIN with ARGS, its output in LOG, and the end of it
# said when it fails.
run() {
	log=$1
	shift
	if ! "$@" >>"$log" 2>&1; then
		echo "upgrade: $*: failed" >&2
		tail -n 20 "$log" >&2
		exit 1
	fi
}

# ask BUILD ARGS...: ARGS asked of the entry point `serve` stood up, as the
# operator whose key this holds: by BUILD's loft on a connection for the
# command alone, or by its loft-admin for a build whose loft-admin still
# asked the entry point itself.
ask() {
	b=$1
	shift
	if "$b/loft-admin" tenant --help >/dev/null 2>&1; then
		"$b/loft-admin" --url "http://$addr" --key "file:$dir/admin.key" "$@"
	else
		"$b/loft" --url "http://$addr" --key "file:$dir/admin.key" "$@"
	fi
}

# serve LOG BUILD: BUILD's entry point on the database the environment
# names, until `unserve`.
served=
serve() {
	LOFT_ADMIN_ADDR=$addr LOFT_ADMIN_KEYS="upgrade=file:$dir/admin.key" "$2/loft-admin" serve >>"$1" 2>&1 &
	served=$!
	i=0
	until ask "$2" tenant ls >/dev/null 2>&1; do
		i=$((i + 1))
		if ! kill -0 "$served" 2>/dev/null || [ "$i" -ge 150 ]; then
			echo "upgrade: $2/loft-admin serve: no answer on $addr" >&2
			tail -n 20 "$1" >&2
			exit 1
		fi
		sleep 0.2
	done
}

unserve() {
	if [ -n "$served" ]; then
		kill "$served" 2>/dev/null || true
		wait "$served" 2>/dev/null || true
		served=
	fi
}

# check NAME: what BEFORE makes on the database the environment names,
# migrated by NOW.
check() {
	trap unserve EXIT
	log=$dir/$1.log
	run "$log" "$before/loft" init --tenant acme --login owner --password owner-pass-1234 --demo
	# A contract -- and, by a build from before a workspace kept its history
	# by its own settings (D36), what it said of that, which the migration
	# moves onto the workspace. On the box, by a build from before the
	# operator's commands were the entry point's.
	keep=forever
	if "$before/loft" contract set --help >/dev/null 2>&1; then
		if "$before/loft" contract set --help 2>&1 | grep -q -- '--keep '; then
			run "$log" "$before/loft" contract set --tenant acme --name before --view 365 --keep 730
			keep=730d
		else
			run "$log" "$before/loft" contract set --tenant acme --name before
		fi
	elif "$before/loft-admin" contract set --help >/dev/null 2>&1; then
		serve "$log" "$before"
		run "$log" ask "$before" contract set --name before --yes acme
		unserve
	else
		serve "$log" "$before"
		run "$log" ask "$before" tenant-contract add --name before --yes acme
		unserve
	fi
	run "$log" "$now/loft" migrate
	moved=$(sed -n "s/^the assets' facts moved into payday's past: \([0-9]*\) rewritten, \([0-9]*\) put on the trail\$/\1 \2/p" "$log")
	if [ -n "$moved" ] && [ "${moved% *}" != "${moved#* }" ]; then
		echo "upgrade: $1: the migration rewrote ${moved% *} facts and put ${moved#* } of them on the trail" >&2
		exit 1
	fi
	# And once more: nothing is left that the first did not do.
	if ! again=$("$now/loft" migrate 2>&1); then
		echo "upgrade: $1: a second migration failed" >&2
		echo "$again" | tail -n 20 >&2
		exit 1
	fi
	held=$(sed -n "s/^what is out holds its time: \([0-9]*\) things.*\$/\1/p" "$log")
	case $again in
	*"moved into payday's past"*)
		echo "upgrade: $1: a second migration rewrote facts again" >&2
		exit 1
		;;
	*"holds its time"*)
		echo "upgrade: $1: a second migration held what is out again" >&2
		exit 1
		;;
	*"in this build's shape"*) ;;
	*)
		echo "upgrade: $1: a second migration still had something to do" >&2
		exit 1
		;;
	esac
	serve "$log" "$now"
	# What NOW says of it, and not what BEFORE said when it wrote it.
	if ! shown=$(ask "$now" tenant show acme 2>&1); then
		echo "upgrade: $1: tenant show: failed" >&2
		echo "$shown" | tail -n 20 >&2
		exit 1
	fi
	echo "$shown" >>"$log"
	kept=$(printf 'keeps\thistory %s,' "$keep")
	case $shown in
	*"$kept"*) ;;
	*)
		echo "upgrade: $1: the workspace does not keep its history for $keep, as its contract said" >&2
		echo "$shown" | tail -n 20 >&2
		exit 1
		;;
	esac
	# Every row what the trail says it is: written through the servers by the
	# build before, and rewritten by the migration on the trail or not at all.
	run "$log" ask "$now" trail rows
	run "$log" ask "$now" retention plan --tenant acme
	run "$log" ask "$now" --reason "an upgrade, checked" tenant export --out "$dir/$1.zip" acme
	run "$log" ask "$now" tenant verify --file "$dir/$1.zip"
	unserve
	echo "upgrade: $1: what the build before made is in this build's shape, and what the trail says of it${moved:+ (${moved% *} facts moved into payday's past)}${held:+ (${held} things out held their time)}"
}

mkdir -p "$dir/sqlite"
(
	export LOFT_DB_DRIVER=sqlite3
	export LOFT_DB_DSN="file:$dir/sqlite/loft.db?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)"
	check sqlite
)

if [ -n "${UPGRADE_POSTGRES:-}" ]; then
	(
		export LOFT_DB_DRIVER=pgx
		export LOFT_DB_DSN="$UPGRADE_POSTGRES/upgrade_loft?sslmode=disable"
		export LOFT_AUTH_ROSTER_DB_DRIVER=pgx
		export LOFT_AUTH_ROSTER_DB_DSN="$UPGRADE_POSTGRES/upgrade_roster?sslmode=disable"
		export LOFT_AUTH_ROSTER_DB_MIGRATE=true
		check postgres
	)
fi
