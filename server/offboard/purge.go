package offboard

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/rove/internal/ent/attachment"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
)

// Purged is what a purge or a retirement took out of a tenant, or would.
type Purged struct {
	Tenant pdid.Id

	// Retired is a tenant that left keeping what the deployment's floors are
	// still on of its trail ([Deployment.Retire]), and not a purge, which
	// keeps nothing.
	Retired bool

	// Rows is the rows taken, by table, the tenant's own row among them.
	Rows map[string]int

	// Files is the stored files that went with their attachments, and Lost the
	// ones that could not be removed and are no row's any more.
	Files int
	Lost  []string

	// Trail is what the trail's purge took: its rows in the database and its
	// namespace in the archive -- and for a retirement, what it kept, by kind,
	// and until when (`Kept`, `Until`).
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

// ErrNoArchive is a tenant that cannot retire here: the deployment keeps
// floors (`audit.min`, `audit.by.<kind>.min`) and no archive, which is where a
// retirement keeps what they are on and the record that it left (payday#115).
// Nothing of it has been taken. It retires once `audit.archive` names one, or
// is purged on an operator's word -- `rove tenant purge`, or a contract that
// says `purge_floors` -- which takes what the floors are on with it.
var ErrNoArchive = errors.New("a tenant retires into the trail's archive, which keeps what the deployment's floors are on, and audit.archive names none")

// Purge takes everything a tenant has out of the deployment: every row of it,
// its files, all of its trail in the database and in the archive -- what the
// deployment's floors are on too (`trail.Policy.PurgeTenant`) -- and last the
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
	return d.leave(ctx, tenant, dry, false)
}

// Retire is [Deployment.Purge] but for the trail the deployment's floors are
// still on (`audit.min`, `audit.by.<kind>.min`): that stays, in the database
// and in the archive, until each floor has passed, and the passes take it then
// (`trail.Policy.RetireTenant`, payday#115) -- the access record PIPA has a
// processor keep for a year, whoever's data it was. Everything else goes as a
// purge's does.
//
// What it keeps of the trail goes on saying what it said of the rows, so it is
// told what became of them: each row a retirement deletes is on the trail as
// gone, the act [Retired], in the transaction that deletes it, as a restore's
// rows are put on it as theirs (payday#108). What no floor is on goes with the
// rest of the trail at the end, and a check of the rows finds the rest as the
// retirement left them, rather than missing.
//
// It needs the archive, which keeps what it keeps and the record that it left:
// with floors and none it is refused, [ErrNoArchive], before anything is
// taken. With no floors it keeps nothing, and is a purge.
func (d Deployment) Retire(ctx context.Context, tenant pdid.Id, dry bool) (Purged, error) {
	return d.leave(ctx, tenant, dry, true)
}

// Leave is whichever of [Deployment.Retire] and [Deployment.Purge] the
// tenant's contract says now (`purge_floors`): a retirement, unless the
// contract in force says to purge and no contract before it that retires is
// still in its grace (`retention.Window.Purge`).
//
// Read when the tenant leaves rather than when it was closed: closing
// destroys nothing, and the floors are what the deployment owes, so the
// contract that counts is the operator's latest word on the law the tenant is
// under -- as the keep windows are read by each pass, and not the day they
// were signed.
func (d Deployment) Leave(ctx context.Context, tenant pdid.Id, dry bool) (Purged, error) {
	w, err := retention.Of(ctx, d.Ent, tenant, d.now(), d.Retention)
	if err != nil {
		return Purged{Tenant: tenant}, err
	}

	return d.leave(ctx, tenant, dry, !w.Purge)
}

// Retired is the act a row a retirement deletes is on the trail as.
const Retired = "rove.workspace.retire"

// Why a destroyed workspace's address is held (`AliasHold.reason`), which
// every destruction writes (`closing.Closing.Destroy`): this deployment's word
// that the workspace was destroyed here, which stays after the hold is over.
// ReasonRetired is one that retired in a deployment that keeps floors, and so
// left a record of it with the trail, which a restore under its identifier
// waits for ([ErrRetiredHere]).
const (
	ReasonDestroyed = "destroyed"
	ReasonRetired   = "retired"
)

// leave is a purge, or a retirement when `retire`.
func (d Deployment) leave(ctx context.Context, tenant pdid.Id, dry, retire bool) (Purged, error) {
	out := Purged{Tenant: tenant, Retired: retire, Rows: map[string]int{}}
	if err := d.held(ctx, tenant); err != nil {
		return out, err
	}
	if retire && d.Trail.Archive == nil && floored(d.Trail) {
		return out, ErrNoArchive
	}

	store := pd.TrailStore(d.Ent)
	take, plan := d.Trail.PurgeTenant, d.Trail.PlanTenantPurge
	if retire {
		take, plan = d.Trail.RetireTenant, d.Trail.PlanTenantRetire
	}
	var err error
	if dry {
		out.Trail, err = plan(ctx, store, tenant)
	} else {
		out.Trail, err = take(ctx, store, tenant)
	}
	if err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}

	keys, err := d.rows(ctx, tenant, dry, retire, out.Rows)
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

	again, err := take(ctx, store, tenant)
	if err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}
	out.Trail.Removed += again.Removed
	out.Trail.Blanked += again.Blanked
	out.Trail.Chunks += again.Chunks
	out.Trail.Rows += again.Rows
	out.Trail.Rewritten += again.Rewritten
	out.Trail.Edited += again.Edited
	out.Trail.Keys += again.Keys
	// What a retirement keeps is what the last of it says it keeps, the
	// first's among it, and until the last's.
	out.Trail.Kept, out.Trail.Until = again.Kept, again.Until

	return out, nil
}

// Floored answers whether the deployment keeps floors of its trail
// (`audit.min`, `audit.by.<kind>.min`), which a retirement keeps what they are
// on for: with none, a retirement keeps nothing and leaves no record, and is a
// purge.
func (d Deployment) Floored() bool { return floored(d.Trail) }

// floored answers whether the deployment keeps a floor of any kind, which a
// retirement keeps what it is on for. payday refuses to retire a tenant with
// floors and no archive in words; this says it before anything is taken, as
// [ErrNoArchive], so that what destroys a closed workspace can tell it from a
// failure.
func floored(p trail.Policy) bool {
	if p.Min.Retain > 0 || p.Min.Destroy > 0 {
		return true
	}
	for _, f := range p.MinBy {
		if f.Retain > 0 || f.Destroy > 0 {
			return true
		}
	}

	return false
}

// rows deletes every row of the tenant, table by table in [Tables]'s order and
// the tenant's own last, in one transaction, and answers the keys of the files
// its attachments kept. For a retirement each is put on the trail as gone, in
// the same transaction ([Deployment.Retire]).
func (d Deployment) rows(ctx context.Context, tenant pdid.Id, dry, retire bool, n map[string]int) ([]string, error) {
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
	gone := []pdid.Id{}
	ids := func(table string) error {
		q, args := sql.Dialect(drv.Dialect()).Select("id").From(sql.Table(table)).Where(sql.EQ("tenant_id", tid)).Query()
		rows := &sql.Rows{}
		if err := drv.Query(ctx, q, args, rows); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("%s: %w", table, err)
			}
			gone = append(gone, pdid.Id(id))
		}
		return rows.Err()
	}
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
		if retire {
			if err := ids(t); err != nil {
				return nil, err
			}
		}
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

	// Gone, as the deployment's work in the tenant: filed under it, where
	// the retirement's last round takes what no floor is on and leaves the
	// rest with what the trail kept.
	if retire {
		gone = append(gone, tenant)
		as := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
		if _, err := pd.RecordErased(as, tx.Client(), d.Audit, Retired, gone); err != nil {
			return nil, fmt.Errorf("the trail of what went: %w", err)
		}
	}

	if d.Unindex != nil {
		if err := d.Unindex(ctx, tx.Client(), tid); err != nil {
			return nil, err
		}
	}

	if dry {
		return keys, nil
	}

	return keys, tx.Commit()
}
