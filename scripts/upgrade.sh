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
# And what was made is still what the deployment says it is, once migrated:
# every row is what the trail's digest of it says (`trail rows`) -- so a
# migration that rewrites rows beneath the servers has put each on the trail
# as it went -- its windows are read (`retention plan`), and a workspace's
# export is taken and checked as this deployment's (`tenant export`, `tenant
# verify`). Both builds are given the keys a deployment's processes have: the
# one the trail's rows are digested with, and the one the trail is signed
# with, which signs an export too.
#
# A migration says what it rewrote, as the move of an asset's facts into
# payday's past does (payday#151), and what it put on the trail of it: the
# same rows, every one. A second has nothing to rewrite.
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
openssl rand -base64 32 >"$dir/digest.key"
openssl genpkey -algorithm ed25519 -out "$dir/checkpoints.pem"
export ROVE_AUDIT_DIGEST_KEY="$dir/digest.key"
export ROVE_AUDIT_CHECKPOINTS_KEY="$dir/checkpoints.pem"

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
	# A contract -- and, by a build from before a workspace kept its history
	# by its own settings (D36), what it said of that, which the migration
	# moves onto the workspace.
	keep=forever
	if "$before" contract set --help 2>&1 | grep -q -- '--keep '; then
		run "$log" "$before" contract set --tenant acme --name before --view 365 --keep 730
		keep=730d
	else
		run "$log" "$before" contract set --tenant acme --name before
	fi
	run "$log" "$now" migrate
	moved=$(sed -n "s/^the assets' facts moved into payday's past: \([0-9]*\) rewritten, \([0-9]*\) put on the trail\$/\1 \2/p" "$log")
	if [ -n "$moved" ] && [ "${moved% *}" != "${moved#* }" ]; then
		echo "upgrade: $1: the migration rewrote ${moved% *} facts and put ${moved#* } of them on the trail" >&2
		exit 1
	fi
	# And once more: nothing is left that the first did not do.
	if ! again=$("$now" migrate 2>&1); then
		echo "upgrade: $1: a second migration failed" >&2
		echo "$again" | tail -n 20 >&2
		exit 1
	fi
	case $again in
	*"moved into payday's past"*)
		echo "upgrade: $1: a second migration rewrote facts again" >&2
		exit 1
		;;
	*"in this build's shape"*) ;;
	*)
		echo "upgrade: $1: a second migration still had something to do" >&2
		exit 1
		;;
	esac
	run "$log" "$now" contract show --tenant acme
	if ! grep -q "history: kept $keep" "$log"; then
		echo "upgrade: $1: the workspace does not keep its history for $keep, as its contract said" >&2
		tail -n 20 "$log" >&2
		exit 1
	fi
	# Every row what the trail says it is: written through the servers by the
	# build before, and rewritten by the migration on the trail or not at all.
	run "$log" "$now" trail rows
	run "$log" "$now" retention plan --tenant acme
	run "$log" "$now" tenant export --tenant acme --out "$dir/$1.zip"
	run "$log" "$now" tenant verify --file "$dir/$1.zip"
	echo "upgrade: $1: what the build before made is in this build's shape, and what the trail says of it${moved:+ (${moved% *} facts moved into payday's past)}"
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
