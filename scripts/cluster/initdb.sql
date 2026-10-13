-- The roster in Rove's process keeps its own database beside Rove's
-- (`auth.roster.db`), as `deploy/compose/initdb/10-roster.sql` makes it.
CREATE DATABASE roster OWNER rove;
