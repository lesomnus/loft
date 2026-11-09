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
# with, which signs an export too. What BEFORE exported of the workspace
# before the migration is restored by NOW, the workspace purged first (`tenant
# import`): an export of the build before is one a restore reads.
#
# BEFORE and NOW are of one compatibility epoch (`.github/compat-epoch`): until
# launch nothing is kept for an earlier build (the owner's decision,
# 2026-11-09), and a change that breaks what one stored or exported moves the
# epoch, after which CI runs no build of the epoch before. So both are asked
# as NOW is.
#
# What an operator does is asked of the entry point (D74): each build's
# `loft-admin serve`, on a port of its own and with an operator's key only
# this holds, asked by its `loft` on an operator's connection (design 9.16).
#
# A second migration has nothing left to do.
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
# operator whose key this holds: by BUILD's loft, on a connection for the
# command alone.
ask() {
	b=$1
	shift
	"$b/loft" --url "http://$addr" --key "file:$dir/admin.key" "$@"
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

# held NAME WAS IS: whether every time a row of the export WAS holds is held
# by the same row of the export IS -- under its own name, or under a name the
# row did not have before, which is the one it was renamed to (D76) -- and
# what it found of each table, in NAME's log. A time a row was last changed
# at is no time it holds. A migration that makes a table again without a
# column it renamed loses what the column held, as SQLite's does of any table
# with an index, and does it without a word: so a time not held is a
# failure, and so is one held otherwise. And the tables the demo's times are
# in are read, or nothing was checked: a booking, a hand-over and a
# placement, each with a time in an index it declares, and what a
# hand-over handed over, a table with indexes and its time in none.
held() {
	log=$dir/$1.log
	compared=
	for f in $(unzip -Z1 "$2" 'rows/*.jsonl'); do
		table=${f#rows/}
		table=${table%.jsonl}
		unzip -p "$2" "$f" >"$dir/$1-was.jsonl"
		unzip -p "$3" "$f" >"$dir/$1-is.jsonl" 2>/dev/null || continue
		said=$(jq -rn --slurpfile was "$dir/$1-was.jsonl" --slurpfile is "$dir/$1-is.jsonl" '
			($is | map({key: .id, value: .}) | from_entries) as $now
			| [$was[] | . as $row | $now[$row.id] as $n | select($n != null)
				| to_entries[]
				| select(.key != "dateUpdated" and (.value | type) == "string" and (.value | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")))
				| . as $e
				| if ($n | has($e.key)) then (if $n[$e.key] == $e.value then "same" else "changed:\($e.key)" end)
				  elif ([$n | to_entries[] | select((.key as $k | $row | has($k) | not) and .value == $e.value)] | length) > 0 then "renamed:\($e.key)"
				  else "lost:\($e.key)" end]
			| group_by(.) | map("\(.[0])=\(length)") | join(" ")')
		echo "held: $table: ${said:-no time}" >>"$log"
		case " $said " in
		*" lost:"* | *" changed:"*)
			echo "upgrade: $1: a time of $table is not what the build before held: $said" >&2
			exit 1
			;;
		esac
		case $table:$said in
		reservation:?* | custody:?* | custodyline:?* | placement:?*) compared="$compared $table" ;;
		esac
	done
	for t in reservation custody custodyline placement; do
		case " $compared " in
		*" $t "*) ;;
		*)
			echo "upgrade: $1: no time of $t was read, so none of it was checked" >&2
			exit 1
			;;
		esac
	done
}

# check NAME: what BEFORE makes on the database the environment names,
# migrated by NOW.
check() {
	trap unserve EXIT
	log=$dir/$1.log
	run "$log" "$before/loft" init --tenant acme --login owner --password owner-pass-1234 --demo
	# A contract, and an export the build before wrote, for this one to
	# restore once it has migrated (below): an export is a contract with
	# whoever kept it (D34).
	exported=$dir/$1-before.zip
	serve "$log" "$before"
	run "$log" ask "$before" tenant-contract add --name before --yes acme
	run "$log" ask "$before" --reason "an upgrade, checked" tenant export --out "$exported" acme
	unserve
	run "$log" "$now/loft" migrate
	# And once more: nothing is left that the first did not do.
	if ! again=$("$now/loft" migrate 2>&1); then
		echo "upgrade: $1: a second migration failed" >&2
		echo "$again" | tail -n 20 >&2
		exit 1
	fi
	case $again in
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
	# Every row what the trail says it is: written through the servers by the
	# build before, and by nothing since.
	run "$log" ask "$now" trail rows
	run "$log" ask "$now" retention plan --tenant acme
	run "$log" ask "$now" --reason "an upgrade, checked" tenant export --out "$dir/$1.zip" acme
	run "$log" ask "$now" tenant verify --file "$dir/$1.zip"
	held "$1" "$exported" "$dir/$1.zip"
	# And what the build before exported, restored by this one into a
	# deployment that does not have the workspace -- so it goes first -- and
	# again what the trail says it is.
	run "$log" ask "$now" tenant verify --file "$exported"
	run "$log" ask "$now" tenant purge --yes acme
	run "$log" ask "$now" tenant import --yes --file "$exported"
	run "$log" ask "$now" tenant show acme
	run "$log" ask "$now" trail rows
	unserve
	echo "upgrade: $1: what the build before made is in this build's shape, and what the trail says of it"
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
