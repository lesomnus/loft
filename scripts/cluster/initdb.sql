-- The roster in Loft's process keeps its own database beside Loft's
-- (`auth.roster.db`), as `deploy/compose/initdb/10-roster.sql` makes it.
CREATE DATABASE roster OWNER loft;
