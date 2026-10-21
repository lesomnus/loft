#!/bin/sh
# A deployment with data in it, brought up to this build (#144): BEFORE, the
# build this one comes after, makes a database with a workspace, its demo and
# a contract in it, and NOW migrates it -- on SQLite, and on PostgreSQL when
# UPGRADE_POSTGRES names a server with the databases upgrade_rove and
# upgrade_roster on it, empty.
#
# Every other test starts from an empty database, which takes any column the
# schema adds; only one with rows in it says whether the column can be added
# to it. A column with no default cannot be, and the migration stops there.
#
#   scripts/upgrade.sh BEFORE NOW
set -eu

before=${1:?usage: scripts/upgrade.sh BEFORE NOW}
now=${2:?usage: scripts/upgrade.sh BEFORE NOW}
dir=$(mktemp -d)
echo "upgrade: the databases and their logs are in $dir"

for v in $(env | sed -n 's/^\(ROVE_[A-Za-z0-9_]*\)=.*/\1/p'); do
	unset "$v"
done
export ROVE_DB_MIGRATE=true
export ROVE_WATCH_BROKER=memory
export ROVE_SPIN_LEASE=local
export ROVE_APP_SIGNING_KEY=rove-upgrade-signing-key-not-a-secret
export ROVE_APP_FILES="$dir/files"

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

# check NAME: what BEFORE makes on the database the environment names,
# migrated by NOW.
check() {
	log=$dir/$1.log
	run "$log" "$before" init --tenant acme --login owner --password owner-pass-1234 --demo
	run "$log" "$before" contract set --tenant acme --name before --view 365 --keep 730
	run "$log" "$now" migrate
	# And once more: nothing is left that the first did not do.
	if ! "$now" migrate 2>&1 | grep -q "in this build's shape"; then
		echo "upgrade: $1: a second migration still had something to do" >&2
		exit 1
	fi
	run "$log" "$now" contract show --tenant acme
	echo "upgrade: $1: what the build before made is in this build's shape"
}

mkdir -p "$dir/sqlite"
(
	export ROVE_DB_DRIVER=sqlite3
	export ROVE_DB_DSN="file:$dir/sqlite/rove.db?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)"
	check sqlite
)

if [ -n "${UPGRADE_POSTGRES:-}" ]; then
	(
		export ROVE_DB_DRIVER=pgx
		export ROVE_DB_DSN="$UPGRADE_POSTGRES/upgrade_rove?sslmode=disable"
		export ROVE_AUTH_ROSTER_DB_DRIVER=pgx
		export ROVE_AUTH_ROSTER_DB_DSN="$UPGRADE_POSTGRES/upgrade_roster?sslmode=disable"
		export ROVE_AUTH_ROSTER_DB_MIGRATE=true
		check postgres
	)
fi
