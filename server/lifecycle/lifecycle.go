// Package lifecycle carries what roster says of an account into this
// deployment (design 9.10, M2 step 5).
//
// roster is where somebody stops being good -- signed out everywhere,
// suspended, erased -- and this deployment holds copies of the decision that
// they were: sessions, and memberships. Each call a session makes is held to
// roster already ([identity.Store.Held]); this is what makes it prompt, from
// roster's own stream ([identity.Store.Follow]), and what an erasure goes on to
// mean here:
//
//   - signed out everywhere: the sessions that began before it end;
//   - suspended or erased: every session of the account ends;
//   - erased: each of its memberships begins to leave (`Holder.date_leaving`),
//     from when roster erased it.
//
// # Leaving, and not gone
//
// Because roster's erasure is not: it destroys nothing for a grace (roster's
// `holder.forget_after`), and `roster restore` brings the account back -- for
// a mistaken deletion, a compromised account deleting things, a billing
// dispute. A membership pseudonymized the moment roster erased its account
// would come back as nobody.
//
// What roster's restore and forget do is not on the stream: both write past
// roster's servers, and the stream carries what they publish. So a later
// event about the account that is not an erasure clears the mark, and
// whatever acts on a mark asks roster first.
package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/session"
)

// Lifecycle is what an account's standing at roster does here.
type Lifecycle struct {
	// Db is this deployment's database, Sessions its sessions on it.
	Db       *ent.Client
	Sessions session.Store

	// Base is the stack with neither the wall nor the domain layer: what a
	// membership's leaving is written through, as the deployment's own work
	// in its workspace, so that the trail says it and a watch hears it.
	Base app.Server
}

// Apply is one event: what roster says of an account now. It carries state and
// not a movement, so applying it twice is applying it once, and an event older
// than what was done before cannot undo it -- a session ended stays ended.
func (l Lifecycle) Apply(ctx context.Context, v identity.Standing) error {
	switch {
	case !v.Erased.IsZero() || !v.Disabled.IsZero():
		if _, err := l.Sessions.End(ctx, v.Holder, time.Time{}); err != nil {
			return err
		}
	case !v.Invalidated.IsZero():
		if _, err := l.Sessions.End(ctx, v.Holder, v.Invalidated); err != nil {
			return err
		}
	}

	hs, err := l.Db.Holder.Query().Where(holder.Account(v.Holder.Uuid()), holder.DateErasedIsNil()).All(ctx)
	if err != nil {
		return err
	}
	for _, h := range hs {
		var req *app.HolderPatchRequest
		switch {
		case !v.Erased.IsZero() && h.DateLeaving == nil:
			req = app.HolderPatchRequest_builder{
				Ref:              app.HolderRef_builder{Id: pdid.Id(h.Id).Bytes()}.Build(),
				DateLeaving:      timestamppb.New(v.Erased),
				DateUpdatedForce: z.Ptr(true),
			}.Build()
		case v.Erased.IsZero() && h.DateLeaving != nil:
			// Restored at roster, which is what the grace was for.
			req = app.HolderPatchRequest_builder{
				Ref:              app.HolderRef_builder{Id: pdid.Id(h.Id).Bytes()}.Build(),
				DateLeavingNull:  z.Ptr(true),
				DateUpdatedForce: z.Ptr(true),
			}.Build()
		default:
			continue
		}
		tenant := pdid.Id(h.TenantId)
		as := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
		if _, err := l.Base.Holder().Patch(as, req); err != nil {
			return fmt.Errorf("holder %s: %w", pdid.Id(h.Id), err)
		}
	}
	return nil
}
