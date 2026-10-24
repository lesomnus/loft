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

	-- And a place holds one thing at a time: a slot of a machine, one part;
	-- a span of a rack's units, one server. A span is inclusive, as `u_from`
	-- and `u_to` are, and zero is no position.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'placement_slot_once' AND conrelid = 'placement'::regclass) THEN
		ALTER TABLE placement ADD CONSTRAINT placement_slot_once
			EXCLUDE USING gist (parent_id WITH =, slot WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL AND slot <> '')
			DEFERRABLE INITIALLY DEFERRED;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'placement_rack_once' AND conrelid = 'placement'::regclass) THEN
		ALTER TABLE placement ADD CONSTRAINT placement_rack_once
			EXCLUDE USING gist (parent_id WITH =, int4range(u_from, u_to, '[]') WITH &&, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL AND u_from > 0)
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

-- A field of a thing -- an entry of a map, of one -- has one value at a moment,
-- among the values known now. Not deferred, since an index cannot be: payday's
-- past supersedes what was recorded for the same moment before it writes the
-- new value (`past.Op.Set`, payday#151), and so is never in between. Made on
-- payday's Fact, which the migration made in place of Rove's own table of
-- facts, and the index on that with it (`cli/facts.go`).
CREATE UNIQUE INDEX IF NOT EXISTS fact_once ON fact (subject_id, field_number, key, valid_from)
	WHERE superseded_at IS NULL;

-- An account is in a workspace once: one holder of it per tenant, among the
-- holders still here (design 9.10, D27). The domain layer checks before it
-- writes, and this is the answer to two writes at once. An index the schema
-- does not know is one its check leaves alone.
CREATE UNIQUE INDEX IF NOT EXISTS holder_account_once ON holder (tenant_id, account)
	WHERE date_erased IS NULL AND account IS NOT NULL;

-- A row names rows of its own tenant only (design 6, D11). The gate refuses a
-- reference into another tenant; this is the answer to SQL that did not go
-- through it -- a script, a hand-run fix, a bug in a layer. A foreign key says
-- that the row named exists and not whose it is, and a composite one, tenant
-- and identifier, would be a constraint the schema check refuses (V1). So every
-- foreign key between two tenanted tables gets a trigger that asks, and answers
-- 23503 as a foreign key would. The keys are read from the catalog, so a table
-- or an edge the schema gains is covered the next time this runs.
CREATE OR REPLACE FUNCTION rove_same_tenant() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
	ref uuid;
	theirs uuid;
BEGIN
	EXECUTE format('SELECT ($1).%I', TG_ARGV[0]) INTO ref USING NEW;
	IF ref IS NULL THEN
		RETURN NEW;
	END IF;
	EXECUTE format('SELECT tenant_id FROM %I.%I WHERE id = $1', TG_TABLE_SCHEMA, TG_ARGV[1]) INTO theirs USING ref;
	-- A row that is not there at all is the foreign key's to refuse.
	IF theirs IS NOT NULL AND theirs <> NEW.tenant_id THEN
		RAISE EXCEPTION '%.% names a % of another tenant', TG_TABLE_NAME, TG_ARGV[0], TG_ARGV[1]
			USING ERRCODE = 'foreign_key_violation';
	END IF;
	RETURN NEW;
END
$$;

DO $$
DECLARE
	fk record;
BEGIN
	FOR fk IN
		SELECT t.relname AS tab, a.attname AS col, r.relname AS ref
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_class r ON r.oid = c.confrelid
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f'
			AND cardinality(c.conkey) = 1
			AND t.relnamespace = current_schema()::regnamespace
			AND a.attname <> 'tenant_id'
			AND EXISTS (SELECT 1 FROM pg_attribute x WHERE x.attrelid = c.conrelid AND x.attname = 'tenant_id' AND NOT x.attisdropped)
			AND EXISTS (SELECT 1 FROM pg_attribute x WHERE x.attrelid = c.confrelid AND x.attname = 'tenant_id' AND NOT x.attisdropped)
	LOOP
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = fk.tab::regclass AND tgname = fk.tab || '_' || fk.col || '_same_tenant') THEN
			EXECUTE format('CREATE TRIGGER %I BEFORE INSERT OR UPDATE OF %I, tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION rove_same_tenant(%L, %L)',
				fk.tab || '_' || fk.col || '_same_tenant', fk.col, fk.tab, fk.col, fk.ref);
		END IF;
	END LOOP;
END
$$;

-- Search is not answered by indexes here any more: a deployment that needs it
-- fast names a search engine (D31), and the database's search reads the
-- tenant's rows. What an earlier migration made for it -- a GIN index a column,
-- pg_bigm's or pg_trgm's, and two prefix B-trees -- is taken out. The
-- extensions stay, as anything else may use them.
DO $$
DECLARE
	c record;
BEGIN
	FOR c IN SELECT * FROM (VALUES
		('asset', 'tag'), ('asset', 'name'), ('asset', 'serial'),
		('assettype', 'name'), ('itemmodel', 'name'), ('itemmodel', 'maker'), ('party', 'name')
	) AS v(tbl, col) LOOP
		EXECUTE format('DROP INDEX IF EXISTS %I', c.tbl || '_' || c.col || '_bigm');
		EXECUTE format('DROP INDEX IF EXISTS %I', c.tbl || '_' || c.col || '_trgm');
	END LOOP;
END
$$;
DROP INDEX IF EXISTS asset_tag_prefix;
DROP INDEX IF EXISTS asset_name_prefix;

-- The way back from a type, a model or a person to the things that are
-- theirs, which listing them goes through. A foreign key PostgreSQL does not
-- index.
CREATE INDEX IF NOT EXISTS asset_type_id ON asset (type_id);
CREATE INDEX IF NOT EXISTS asset_model_id ON asset (model_id);
CREATE INDEX IF NOT EXISTS asset_custodian_id ON asset (custodian_id);

-- No CHECK constraints here, though "stock is never negative" wants one: the
-- schema check `serve` runs without `db.migrate` sees a CHECK it was not told
-- about and refuses the database (lesomnus/payday#33). The EXCLUDE
-- constraints above it does not see. The domain layer refuses a negative
-- stock on its own, inside the transaction that would make it.
