#!/usr/bin/env bash
# doctor.sh -- what `pd doctor` says would go wrong, failing on what it says
# that is not known here.
#
#   scripts/doctor.sh [DIR]   # the app; the working directory by default
#
# pd doctor answers 0 for advice and fails only for what would stop a
# generation, and it cannot be told which of its advice an app has read
# already: run as it is, it passes whatever it finds. So this keeps that list.
# What doctor says of each thing is a line, its first, and the rest of it --
# the fix -- is indented under it; `known` below is the first lines read
# already, word for word, each with what it is. Anything else doctor says
# fails, and so does anything that stops a generation. One of `known` it no
# longer says is a notice, for the list to lose it, and fails nothing.
#
# Every finding is said whole first, as doctor says it, so that what failed
# is read where it failed.
set -euo pipefail

dir=${1:-.}

# What doctor said on main at 93e0b6d (rove#184's follow-ups): three kinds
# kept off the trail that hold a secret, so that `rove trail rows` finds no
# row of them added in the database past the servers. Each says why it is off
# in its schema (`proto/rove/org.proto`). Whether a way in should be recorded
# instead, as doctor suggests, is open; these are known, not decided.
#
# And since a workspace keeps why it was closed as personal (`close_reason`,
# design D56): payday's own fields of its Tenant, 4 to 7, which an overlay may
# not declare -- `pd gen` refuses one that changes a number of payday's --
# and which payday's Tenant leaves undeclared. Which they are is payday's to
# say. Rove writes nobody into them (D57): the workspace an account is given
# when it arrives has no name, and is called after its owner where it is read
# (`server/tenancy`). Known, until payday says they are about no one.
known=(
	# A code a browser carries to a workspace's domain, good once for a minute.
	"rove.Handoff is kept off the trail and holds a secret (secret, state), so nothing finds one of its rows added past the servers"
	# A sign-in, whose idle clock moves on every request.
	"rove.Session is kept off the trail and holds a secret (secret, held), so nothing finds one of its rows added past the servers"
	# The sign-ins an address got wrong, kept for hours to slow the next.
	"rove.SignInFailure is kept off the trail and holds a secret (subject), so nothing finds one of its rows added past the servers"
	# payday's Tenant's alias, name, desc and labels, which only payday declares.
	"rove.Tenant holds people and says nothing of alias, name, desc, labels"
)

# What it says goes to stdout and what stops it to stderr, which is left to
# say itself.
if out=$(go tool pd doctor "$dir"); then
	rc=0
else
	rc=$?
fi
if [ -n "$out" ]; then
	printf '%s\n' "$out"
fi

# The first line of each finding: one that is not indented, and not the line
# doctor says when it has nothing to say.
said=()
while IFS= read -r line; do
	case $line in
	'' | [[:space:]]*) continue ;;
	'pd: '*' looks like an app that generates') continue ;;
	esac
	said+=("$line")
done <<<"$out"

is() {
	local v=$1
	shift
	local w
	for w in "$@"; do
		[ "$v" = "$w" ] && return 0
	done
	return 1
}

new=()
for v in ${said[@]+"${said[@]}"}; do
	is "$v" "${known[@]}" || new+=("$v")
done
gone=()
for v in "${known[@]}"; do
	is "$v" ${said[@]+"${said[@]}"} || gone+=("$v")
done

echo
for v in ${gone[@]+"${gone[@]}"}; do
	# A notice in a GitHub Actions run, a line anywhere else.
	if [ -n "${GITHUB_ACTIONS:-}" ]; then
		echo "::notice title=pd doctor::no longer said, so scripts/doctor.sh can stop knowing it: $v"
	else
		echo "doctor.sh: no longer said, so scripts/doctor.sh can stop knowing it: $v"
	fi
done
if [ "$rc" -ne 0 ]; then
	echo "doctor.sh: pd doctor says a generation would stop (exit $rc)" >&2
	exit 1
fi
if [ ${#new[@]} -gt 0 ]; then
	echo "doctor.sh: pd doctor says what is not known here -- fix it, or add its first line to \`known\` with what it is:" >&2
	printf '  %s\n' "${new[@]}" >&2
	exit 1
fi
if [ ${#said[@]} -eq 0 ]; then
	echo "doctor.sh: pd doctor found nothing"
else
	echo "doctor.sh: pd doctor found only what is known here (${#said[@]})"
fi
