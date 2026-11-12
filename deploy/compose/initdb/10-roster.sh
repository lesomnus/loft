#!/bin/sh
# roster's own user and its two databases -- the data plane and the control
# plane -- beside Loft's, so that Loft's user reaches none of roster's rows.
# Run once, by postgres' entrypoint, when the volume is new. The password is
# ROSTER_DB_PASSWORD from .env, letters and digits only.
set -eu
psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" <<SQL
CREATE ROLE roster LOGIN PASSWORD '${ROSTER_DB_PASSWORD}';
CREATE DATABASE roster OWNER roster;
CREATE DATABASE roster_control OWNER roster;
SQL
