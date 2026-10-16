# Rove as a deployment runs it: `rove` and `rove-admin`, and the built UI they
# serve, on an image with nothing else in it (`deploy/k8s`, and `deploy/compose`
# to try it on one machine).
#
#   docker build -t rove --build-arg VERSION=$(git describe --tags --always) .
#
# Everything it is told comes from the environment (`ROVE_*`) or a file named
# with `--config`; there is no `rove.yaml` in it, so a checkout's development
# defaults never reach a deployment.

# The page: `ts/gen` is generated and checked in, so this is the page alone.
FROM node:22-bookworm-slim AS web
WORKDIR /src/ts
COPY ts/package.json ts/package-lock.json ./
RUN npm ci
COPY ts/ ./
RUN npm run build

# The binaries, static: the SQLite driver is WebAssembly in Go and asks for no
# C library, so neither does anything else. The toolchain stamps the commit
# from the repository, and VERSION names the release (`rove version`).
#
# The patch release, named: the standard library is linked into the binary,
# so its fixes arrive only by building with the release that has them -- and
# this image's Go is the one that builds (it does not fetch the `toolchain`
# go.mod names). The two say the same release (rove#40).
FROM golang:1.27.2-bookworm AS go
ARG VERSION=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The build cache is a cache mount, so that building again compiles what
# changed and not the rest -- a desk's next build, and CI's, which carries it
# from one run to the next by this id (scripts/gocache.sh).
RUN --mount=type=cache,id=rove-go-build,target=/root/.cache/go-build \
	flags="-s -w -X github.com/lesomnus/payday/version.version=${VERSION}" \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="${flags}" -o /out/rove ./cmd/rove \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="${flags}" -o /out/rove-admin ./cmd/rove-admin
# Where a deployment's state goes, made here so that a volume mounted on them
# starts out owned by the user below.
RUN mkdir -p /out/state/files /out/state/audit /out/state/checkpoints

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/rove /out/rove-admin /usr/local/bin/
COPY --from=web /src/ts/dist /srv/rove/web
COPY --from=go --chown=nonroot:nonroot /out/state /var/lib/rove
ENV ROVE_APP_WEB=/srv/rove/web \
	ROVE_APP_FILES=/var/lib/rove/files
WORKDIR /var/lib/rove
USER nonroot:nonroot
# 8080 is a browser's (and `rove api`'s), 50051 gRPC's, 8081 the operator's
# entry point (`rove-admin serve`), which is never published to the public.
EXPOSE 8080 50051 8081
ENTRYPOINT ["/usr/local/bin/rove"]
CMD ["serve"]
