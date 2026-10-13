package offboard

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/rove/internal/ent/attachment"
	"github.com/lesomnus/rove/server/pd"
)

// Purged is what a purge took out of a tenant, or would.
type Purged struct {
	Tenant pdid.Id

	// Rows is the rows taken, by table, the tenant's own row among them.
	Rows map[string]int

	// Files is the stored files that went with their attachments, and Lost the
	// ones that could not be removed and are no row's any more.
	Files int
	Lost  []string

	// Trail is what the trail's purge took: its rows in the database and its
	// namespace in the archive.
	Trail trail.TenantPurge
}

// Total is the rows taken, the trail's aside.
func (x Purged) Total() int {
	n := 0
	for _, v := range x.Rows {
		n += v
	}
	return n
}

// errDry undoes a purge that was only counting.
var errDry = errors.New("offboard: a dry run")

// Purge takes everything a tenant has out of the deployment: every row of it,
// its files, its trail in the database and in the archive, and last the
// tenant itself. `dry` does the database's part in a transaction it then
// undoes, and asks the trail what it would take, so that what it says is what
// a purge does.
//
// A tenant under a legal hold is refused, which is [trail.ErrHeld].
//
// The trail goes first and once more at the end. A purge that fails after the
// first is one to run again, and the second takes what was written while the
// rows went -- the tenant is still somebody's to sign into until its holders
// are gone.
func (d Deployment) Purge(ctx context.Context, tenant pdid.Id, dry bool) (Purged, error) {
	out := Purged{Tenant: tenant, Rows: map[string]int{}}
	if err := d.held(ctx, tenant); err != nil {
		return out, err
	}

	store := pd.TrailStore(d.Ent)
	var err error
	if dry {
		out.Trail, err = d.Trail.PlanTenantPurge(ctx, store, tenant)
	} else {
		out.Trail, err = d.Trail.PurgeTenant(ctx, store, tenant)
	}
	if err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}

	keys, err := d.rows(ctx, tenant, dry, out.Rows)
	if err != nil {
		return out, err
	}
	if dry {
		out.Files = len(keys)
		return out, nil
	}

	for _, k := range keys {
		if d.Files == nil {
			out.Lost = append(out.Lost, k)
			continue
		}
		if err := d.Files.Delete(ctx, k); err != nil {
			out.Lost = append(out.Lost, k)
			continue
		}
		out.Files++
	}

	again, err := d.Trail.PurgeTenant(ctx, store, tenant)
	if err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}
	out.Trail.Removed += again.Removed
	out.Trail.Blanked += again.Blanked
	out.Trail.Chunks += again.Chunks
	out.Trail.Rows += again.Rows
	out.Trail.Rewritten += again.Rewritten
	out.Trail.Edited += again.Edited

	return out, nil
}

// rows deletes every row of the tenant, table by table in [Tables]'s order and
// the tenant's own last, in one transaction, and answers the keys of the files
// its attachments kept.
func (d Deployment) rows(ctx context.Context, tenant pdid.Id, dry bool, n map[string]int) ([]string, error) {
	ts, err := Tables()
	if err != nil {
		return nil, err
	}

	tx, err := d.Ent.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	tid := tenant.Uuid()
	keys, err := tx.Attachment.Query().Where(attachment.TenantId(tid)).Select(attachment.FieldObjectKey).Strings(ctx)
	if err != nil {
		return nil, err
	}

	drv := tx.Client().Driver()
	del := func(table, column string, id uuid.UUID) (int, error) {
		q, args := sql.Dialect(drv.Dialect()).Delete(table).Where(sql.EQ(column, id)).Query()
		var res stdsql.Result
		if err := drv.Exec(ctx, q, args, &res); err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		v, err := res.RowsAffected()
		return int(v), err
	}
	for _, t := range ts {
		v, err := del(t, "tenant_id", tid)
		if err != nil {
			return nil, err
		}
		if v > 0 {
			n[t] = v
		}
	}
	v, err := del("tenant", "id", tid)
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, fmt.Errorf("no tenant %s", tenant)
	}
	n["tenant"] = v

	if dry {
		return keys, nil
	}

	return keys, tx.Commit()
}
