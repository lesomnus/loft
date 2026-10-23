package domain

import (
	"context"
	"time"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/retention"
)

// How long a workspace keeps what it was and did is its own setting, its
// owner's to say through Configure as the rest of them (design 8, D36): its
// history, its access record, and how long it may be restored once closed.
// What is kept is what is seen -- nothing narrows a read to less of it.
//
// A window made shorter waits out the deployment's grace (`app.retention.
// grace`) before it is the shorter one, holding meanwhile to the longer one it
// had ([retention.Setting]), so that a mistake undone in time has destroyed
// nothing; and Preview says, before anything is saved, what a change would
// take and from when.

// maxKeepDays is the longest a window may be said in days: a century, which
// is past forever for anything here and short of what a duration holds. 0 is
// forever.
const maxKeepDays = 100 * 365

// retention is the deployment's half of a workspace's windows.
func (d *Deps) retention() retention.Defaults {
	if d == nil {
		return retention.Defaults{}
	}

	return d.Retention
}

// checkKeep refuses windows a workspace may not say: longer than a century,
// and an access record shorter than the deployment's floor -- what the law
// the deployment is under asks it to keep, whatever a workspace says.
func (d *Deps) checkKeep(keep, access, restore *uint32) error {
	for _, f := range []struct {
		name string
		v    *uint32
	}{{"keep_days", keep}, {"access_keep_days", access}, {"restore_days", restore}} {
		if f.v != nil && *f.v > maxKeepDays {
			return invalid(f.name, "기간은 %d일(100년)까지 정합니다. 영원히 두려면 0을 넣으세요", maxKeepDays)
		}
	}
	if access != nil && *access > 0 {
		if floor := d.retention().AccessFloor; retention.Days(*access) < floor {
			return invalid("access_keep_days", "접속기록은 법이 정한 %d일보다 짧게 둘 수 없습니다", daysOf(floor))
		}
	}

	return nil
}

// daysOf is a window in whole days.
func daysOf(v time.Duration) uint32 {
	return uint32(v / (24 * time.Hour))
}

// days is what a request says of a window, nil for nothing.
func days(has bool, v uint32) *uint32 {
	if !has {
		return nil
	}

	return &v
}

// keeping is the workspace's windows once `keep` and `access` are said, nil
// for what is left as it is.
func (t *Tx) keeping(keep, access *uint32) (retention.Setting, retention.Setting, error) {
	cur, err := t.db.Tenant.Get(t.ctx, t.tenant.Uuid())
	if err != nil {
		return retention.Setting{}, retention.Setting{}, err
	}

	d := t.deps.retention()
	k, a := retention.KeepOf(cur), retention.AccessOf(cur)
	if keep != nil {
		k = k.Set(*keep, t.now, d.Wait(), retention.Days)
	}
	if access != nil {
		a = a.Set(*access, t.now, d.Wait(), d.AccessDays)
	}

	return k, a, nil
}

// keepPatch writes into `patch` the windows a Configure says, and into
// `said` what they came to for its event: a shorter one, from when it is.
func (t *Tx) keepPatch(req *app.TenantConfigureRequest, patch *app.TenantPatchRequest, said *app.WorkspaceConfigured) error {
	keep, access := days(req.HasKeepDays(), req.GetKeepDays()), days(req.HasAccessKeepDays(), req.GetAccessKeepDays())
	if keep != nil || access != nil {
		k, a, err := t.keeping(keep, access)
		if err != nil {
			return err
		}

		d := t.deps.retention()
		if keep != nil {
			patch.SetKeepDays(k.Days)
			patch.SetKeepDaysBefore(k.Before)
			if k.Shortened != nil {
				patch.SetDateKeepShortened(ts(*k.Shortened))
			} else {
				patch.SetDateKeepShortenedNull(true)
			}
			said.SetKeepDays(k.Days)
			if u := k.Until(t.now, d.Wait(), retention.Days); !u.IsZero() {
				said.SetKeepFrom(ts(u))
			}
		}
		if access != nil {
			patch.SetAccessKeepDays(a.Days)
			patch.SetAccessKeepDaysBefore(a.Before)
			if a.Shortened != nil {
				patch.SetDateAccessKeepShortened(ts(*a.Shortened))
			} else {
				patch.SetDateAccessKeepShortenedNull(true)
			}
			said.SetAccessKeepDays(a.Days)
			if u := a.Until(t.now, d.Wait(), d.AccessDays); !u.IsZero() {
				said.SetAccessKeepFrom(ts(u))
			}
		}
	}
	if req.HasRestoreDays() {
		patch.SetRestoreDays(req.GetRestoreDays())
		said.SetRestoreDays(req.GetRestoreDays())
	}

	return nil
}

// Preview is what a change of how long the workspace keeps its history and its
// access record would come to, and from when, changing nothing: what its owner
// is shown before Configure saves it (D36).
//
// What the history's window takes is what a retention pass would take then, as
// the history is now -- what was over before the window as it will be once the
// grace is over, and nothing that ends between now and then -- counted by the
// predicates the expiry deletes by ([Tx.takes]). Counted, and not done and
// undone as `rove retention plan` does it: a deletion holds the rows it took
// until it is undone, and this is asked by a workspace's owner, as often as
// they like, of however much history the workspace has. So it is a read, with
// no transaction of its own.
func (s domainTenant) Preview(ctx context.Context, req *app.TenantPreviewRequest) (*app.TenantPreviewResponse, error) {
	keep, access := days(req.HasKeepDays(), req.GetKeepDays()), days(req.HasAccessKeepDays(), req.GetAccessKeepDays())
	if err := s.deps.checkKeep(keep, access, nil); err != nil {
		return nil, err
	}

	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	if err := t.mine(req.GetRef()); err != nil {
		return nil, err
	}
	k, a, err := t.keeping(keep, access)
	if err != nil {
		return nil, err
	}
	w, err := t.window()
	if err != nil {
		return nil, err
	}

	d := t.deps.retention()
	wait := d.Wait()
	from := func(s retention.Setting, days func(uint32) time.Duration) time.Time {
		if u := s.Until(t.now, wait, days); !u.IsZero() {
			return u
		}
		return t.now
	}
	out := app.TenantPreviewResponse_builder{
		Apply:           d.Apply,
		Held:            w.Held(),
		GraceDays:       daysOf(wait),
		KeepDays:        k.Days,
		KeepFrom:        ts(from(k, retention.Days)),
		AccessKeepDays:  daysOf(d.AccessDays(a.Days)),
		AccessKeepFrom:  ts(from(a, d.AccessDays)),
		AccessFloorDays: daysOf(d.AccessFloor),
	}.Build()
	if k.Days == 0 {
		return out, nil
	}

	cut := from(k, retention.Days).Add(-retention.Days(k.Days))
	if cut.After(t.now) {
		cut = t.now
	}
	x, err := t.takes(cut)
	if err != nil {
		return nil, err
	}
	takes := map[string]uint32{}
	for kind, n := range map[string]int{
		"placements": x.Placements, "links": x.Links, "stewardships": x.Stewardships, "facts": x.Facts, "events": x.Events,
		"reservations": x.Reservations, "custodies": x.Custodies, "counts": x.Counts, "work_orders": x.WorkOrders,
		"stock_movements": x.StockMovements, "attachments": x.Attachments,
	} {
		if n > 0 {
			takes[kind] = uint32(n)
		}
	}
	out.SetKeepTakes(takes)

	return out, nil
}
