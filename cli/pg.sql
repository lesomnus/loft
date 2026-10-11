-- What PostgreSQL can promise that the schema cannot say (design 9.4).
--
-- The domain layer already refuses all of this; these are the second answer,
-- for the day it has a bug. Every statement is safe to run again, so this
-- runs after every migration rather than being tracked.
--
-- The constraints are deferred to the end of the transaction because the
-- domain layer writes a change as "supersede the old row, then insert the new
-- ones", and in between the two are briefly both current.

-- In `public`, where every schema of the database finds it: an extension is
-- one per database, and a schema that is dropped -- a test's -- must not take
-- it along. Two processes migrating at once race on it, and the loser finds
-- it made.
DO $$
BEGIN
	CREATE EXTENSION IF NOT EXISTS btree_gist SCHEMA public;
EXCEPTION WHEN unique_violation THEN
	NULL;
END
$$;

-- Each is asked after on its own table, which is the one this schema's
-- search path finds: `pg_constraint` is the whole database's, and a name
-- another schema's table has is not a constraint here. Two schemas in one
-- database -- every PostgreSQL test has its own -- otherwise left the second
-- with none of them.
DO $$
BEGIN
	-- Two blocking allocations of one exclusive resource never overlap: a
	-- room is not booked twice, and not booked while it is being repaired.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'allocation_exclusive_no_overlap' AND conrelid = 'allocation'::regclass) THEN
		ALTER TABLE allocation ADD CONSTRAINT allocation_exclusive_no_overlap
			EXCLUDE USING gist (resource_id WITH =, tstzrange(begins_at, ends_at, '[)') WITH &&)
			WHERE (blocking AND exclusive)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- A thing is in one place at a time, as far as is known now.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'placement_one_parent' AND conrelid = 'placement'::regclass) THEN
		ALTER TABLE placement ADD CONSTRAINT placement_one_parent
			EXCLUDE USING gist (child_id WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- One owner, one manager and one custodian at a time.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'stewardship_one_per_role' AND conrelid = 'stewardship'::regclass) THEN
		ALTER TABLE stewardship ADD CONSTRAINT stewardship_one_per_role
			EXCLUDE USING gist (asset_id WITH =, role WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- A relation between two things holds or does not, once at a time.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'link_once' AND conrelid = 'link'::regclass) THEN
		ALTER TABLE link ADD CONSTRAINT link_once
			EXCLUDE USING gist (source_id WITH =, target_id WITH =, kind WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;
END
$$;

-- An account is in a workspace once: one holder of it per tenant, among the
-- holders still here (design 9.10, D27). The domain layer checks before it
-- writes, and this is the answer to two writes at once. An index the schema
-- does not know is one its check leaves alone.
CREATE UNIQUE INDEX IF NOT EXISTS holder_account_once ON holder (tenant_id, account)
	WHERE date_erased IS NULL AND account IS NOT NULL;

-- Search (design 9.5, `internal/search`). What a person types and what is kept
-- are compared as `lower(normalize(x, NFKC))`, full-width and half-width
-- forms alike. pg_bigm answers that from two letters -- `의자` is two -- and
-- where it is not installed pg_trgm, which every PostgreSQL has, from three;
-- a shorter question goes by prefix on the tag and the name, which the B-trees
-- at the end answer. Which of the two this database has is decided here, and
-- the server reads it back.
--
-- pg_bigm is not a trusted extension: a role that is not a superuser cannot
-- create it, and the database's owner does, once (`CREATE EXTENSION pg_bigm`).
-- Until then this goes on with pg_trgm, which is trusted.
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_bigm') THEN
		BEGIN
			CREATE EXTENSION IF NOT EXISTS pg_bigm SCHEMA public;
		EXCEPTION
			WHEN unique_violation THEN
				NULL;
			WHEN insufficient_privilege THEN
				RAISE NOTICE 'pg_bigm is installed, and only a superuser may create it: CREATE EXTENSION pg_bigm';
		END;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_bigm')
		AND EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_trgm') THEN
		BEGIN
			CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;
		EXCEPTION
			WHEN unique_violation OR insufficient_privilege THEN
				NULL;
		END;
	END IF;
END
$$;

-- A GIN index a column, of whichever kind the database has, named for it so
-- that a database which gains pg_bigm drops what pg_trgm answered with.
DO $$
DECLARE
	kind text;
	other text;
	ops text;
	c record;
BEGIN
	IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_bigm') THEN
		kind := 'bigm'; other := 'trgm'; ops := 'public.gin_bigm_ops';
	ELSIF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
		kind := 'trgm'; other := 'bigm'; ops := 'public.gin_trgm_ops';
	END IF;
	FOR c IN SELECT * FROM (VALUES
		('asset', 'tag'), ('asset', 'name'), ('asset', 'serial'),
		('assettype', 'name'), ('itemmodel', 'name'), ('itemmodel', 'maker'), ('party', 'name')
	) AS v(tbl, col) LOOP
		IF kind IS NULL THEN
			EXIT;
		END IF;
		EXECUTE format('DROP INDEX IF EXISTS %I', c.tbl || '_' || c.col || '_' || other);
		EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I USING gin (lower(normalize(%I, NFKC)) %s)',
			c.tbl || '_' || c.col || '_' || kind, c.tbl, c.col, ops);
	END LOOP;
END
$$;

CREATE INDEX IF NOT EXISTS asset_tag_prefix ON asset (tenant_id, lower(normalize(tag, NFKC)) text_pattern_ops);
CREATE INDEX IF NOT EXISTS asset_name_prefix ON asset (tenant_id, lower(normalize(name, NFKC)) text_pattern_ops);

-- And the way back from a type, a model or a person to the things that are
-- theirs, which a search by what something is or who has it goes through --
-- as does listing them. A foreign key PostgreSQL does not index.
CREATE INDEX IF NOT EXISTS asset_type_id ON asset (type_id);
CREATE INDEX IF NOT EXISTS asset_model_id ON asset (model_id);
CREATE INDEX IF NOT EXISTS asset_custodian_id ON asset (custodian_id);

-- No CHECK constraints here, though "stock is never negative" wants one: the
-- schema check `serve` runs without `db.migrate` sees a CHECK it was not told
-- about and refuses the database (lesomnus/payday#33). The EXCLUDE
-- constraints above it does not see. The domain layer refuses a negative
-- stock on its own, inside the transaction that would make it.
