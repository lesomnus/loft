#!/usr/bin/env bash
# gocache.sh -- the image's Go build cache, carried from one CI run to the
# next.
#
#   scripts/gocache.sh load DIR   # into the builder, before the image is built
#   scripts/gocache.sh save DIR   # out of it again, after
#
# The Dockerfile compiles with its build cache on a cache mount (`id=rove-go-build`),
# which BuildKit keeps in the builder -- and a runner's builder is new every
# run, so every job that built the image compiled rove from nothing: 100 to
# 130 seconds of each. `load` copies DIR into that mount and `save` copies the
# mount out to DIR, which actions/cache keeps in between, as it keeps the `go`
# job's own.
#
# Each is a build of its own, on the builder the image is built on (compose's
# too), that mounts the same cache. Neither may be built with `--no-cache`:
# that gives a build cache mounts of its own rather than the builder's, and
# what it copied would go nowhere. A build argument that is new every time is
# what makes them run rather than be found in the builder's cache.
set -euo pipefail

: "${ALPINE_IMAGE:=alpine:3.22}"

dir=${2:-}
if [ -z "$dir" ]; then
	echo "usage: scripts/gocache.sh load | save DIR" >&2
	exit 2
fi
run=$(date +%s%N)

case ${1:-} in
load)
	# Nothing kept, as on the first run, is a build from nothing.
	if [ ! -d "$dir" ]; then
		echo "gocache: nothing in $dir"
		exit 0
	fi
	docker build -q --build-arg "run=$run" -f - "$dir" > /dev/null <<EOF
FROM $ALPINE_IMAGE
ARG run
RUN --mount=type=cache,id=rove-go-build,target=/cache --mount=type=bind,target=/kept cp -a /kept/. /cache/
EOF
	;;
save)
	empty=$(mktemp -d)
	trap 'rm -rf "$empty"' EXIT
	rm -rf "$dir"
	docker build -q --build-arg "run=$run" -o "type=local,dest=$dir" -f - "$empty" > /dev/null <<EOF
FROM $ALPINE_IMAGE AS kept
ARG run
RUN --mount=type=cache,id=rove-go-build,target=/cache mkdir /kept && cp -a /cache/. /kept/
FROM scratch
COPY --from=kept /kept /
EOF
	;;
*)
	echo "usage: scripts/gocache.sh load | save DIR" >&2
	exit 2
	;;
esac
echo "gocache: $1 $dir, $(du -sh "$dir" | cut -f1)"
