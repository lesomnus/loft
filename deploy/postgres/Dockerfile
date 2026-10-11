# PostgreSQL with pg_bigm, which is what answers a search for two letters of
# Korean (design 9.5). The official image, with the extension built from its
# release beside it: nothing else about the server changes.
#
#   docker build -t rove-postgres deploy/postgres
#
# Without pg_bigm Rove still searches -- with pg_trgm from three letters and
# by prefix below that -- so a managed database that does not offer it is a
# slower search and not a refusal.
ARG PG_MAJOR=18

FROM postgres:${PG_MAJOR} AS build
ARG PG_MAJOR
# The release, and what it has to hash to: a tag can be moved and a tarball
# replaced, a digest cannot.
ARG PG_BIGM_VERSION=1.2-20250903
ARG PG_BIGM_SHA256=4d4fb48161e3f9e99207851299379c83b3e5d1d3c1a07c35f6a3153d577b3142
RUN apt-get update \
	&& apt-get install -y --no-install-recommends build-essential ca-certificates curl libicu-dev "postgresql-server-dev-${PG_MAJOR}" \
	&& rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/pg_bigm.tar.gz "https://github.com/pgbigm/pg_bigm/archive/refs/tags/v${PG_BIGM_VERSION}.tar.gz" \
	&& echo "${PG_BIGM_SHA256}  /tmp/pg_bigm.tar.gz" | sha256sum -c - \
	&& tar xzf /tmp/pg_bigm.tar.gz -C /tmp \
	&& make -C "/tmp/pg_bigm-${PG_BIGM_VERSION}" USE_PGXS=1 \
	&& make -C "/tmp/pg_bigm-${PG_BIGM_VERSION}" USE_PGXS=1 install DESTDIR=/out

FROM postgres:${PG_MAJOR}
COPY --from=build /out/ /
