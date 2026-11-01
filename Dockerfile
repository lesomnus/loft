# Loft as a deployment runs it: `loft` and `loft-admin`, and the built UI they
# serve, on an image with nothing else in it (`deploy/k8s`, and `deploy/compose`
# to try it on one machine).
#
#   docker build -t loft --build-arg VERSION=$(git describe --tags --always) .
#
# Everything it is told comes from the environment (`LOFT_*`) or a file named
# with `--config`; there is no `loft.yaml` in it, so a checkout's development
# defaults never reach a deployment.

# The page: `ts/gen` is generated and checked in, so this is the page alone.
FROM node:26-bookworm-slim AS web
WORKDIR /src/ts
COPY ts/package.json ts/package-lock.json ./
RUN npm ci
COPY ts/ ./
RUN npm run build

# The binaries, static: the SQLite driver is WebAssembly in Go and asks for no
# C library, so neither does anything else. The toolchain stamps the commit
# from the repository, and VERSION names the release (`loft version`).
#
# The patch release, named: the standard library is linked into the binary,
# so its fixes arrive only by building with the release that has them -- and
# this image's Go is the one that builds (it does not fetch the `toolchain`
# go.mod names). The two say the same release (loft#40).
FROM golang:1.27.3-bookworm AS go
ARG VERSION=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The build cache is a cache mount, so that building again compiles what
# changed and not the rest -- a desk's next build, and CI's, which carries it
# from one run to the next by this id (scripts/gocache.sh).
RUN --mount=type=cache,id=loft-go-build,target=/root/.cache/go-build \
	flags="-s -w -X github.com/lesomnus/payday/version.version=${VERSION}" \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="${flags}" -o /out/loft ./cmd/loft \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="${flags}" -o /out/loft-admin ./cmd/loft-admin
# Where a deployment's state goes, made here so that a volume mounted on them
# starts out owned by the user below -- the trail's store witness among them
# (payday#115), which a named volume would otherwise give to root.
RUN mkdir -p /out/state/files /out/state/audit /out/state/checkpoints /out/state/witness

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/loft /out/loft-admin /usr/local/bin/
COPY --from=web /src/ts/dist /srv/loft/web
COPY --from=go --chown=nonroot:nonroot /out/state /var/lib/loft
ENV LOFT_APP_WEB=/srv/loft/web \
	LOFT_APP_FILES=/var/lib/loft/files
WORKDIR /var/lib/loft
USER nonroot:nonroot
# 8080 is a browser's (and `loft api`'s), 50051 gRPC's, 8081 the operator's
# entry point (`loft-admin serve`), which is never published to the public.
EXPOSE 8080 50051 8081
ENTRYPOINT ["/usr/local/bin/loft"]
CMD ["serve"]
