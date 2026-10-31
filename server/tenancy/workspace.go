package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
)

// ErrOwnsEnough is an account that owns as many workspaces as it may: one
// more is one somebody pays for (design 9.11).
var ErrOwnsEnough = errors.New("this account owns as many workspaces as it may without paying for another")

// Workspaces is how an account comes to be in a workspace of its own: the one
// it is given when it is in none, and the ones it makes.
type Workspaces struct {
	// Base is the stack with no wall and no domain layer, and Drv the driver
	// it writes through: what puts up a workspace and its owner, before there
	// is anybody to act as.
	Base app.Server
	Drv  dialect.Driver

	// Ungated is the stack with the domain layer and no wall, which a new
	// workspace's setup is written through as its owner.
	Ungated app.Server

	// Personal says whether an account in no workspace is given one when it
	// arrives.
	Personal bool
	// Owned is how many workspaces one account may own; negative is no limit.
	Owned int

	// Aliases is how a new workspace's address is made up.
	Aliases Aliases

	// Told is told of every workspace made, in the transaction that makes
	// it: who made it and when, which levyan is to hear as who pays for it
	// (`levyan.Notices`, levyan#14). Nil tells nobody, as a deployment that
	// bills nobody does.
	Told Told
}

// Told is what hears of a workspace made, written in the transaction that
// makes it -- on `db`, a client of that transaction -- so that a workspace
// that is there is one it heard of, and one rolled back is not.
type Told interface {
	Arrive(ctx context.Context, db *ent.Client, workspace, creator pdid.Id, created time.Time) error
}

// Membership is a workspace an account is in, and what it does there.
type Membership struct {
	Workspace pdid.Id
	Alias     string
	// Name is what the workspace is called ([Names]), and Saw whose personal
	// data saying it shows: the owner one with no name of its own is called
	// after, which whatever shows it puts on the access record.
	Name   string
	Saw    []access.Subject
	Holder pdid.Id
	Role   string

	// Closed is when the workspace was closed, and Destroy when it goes if
	// it is not restored (D29); nil for one that is open.
	Closed, Destroy *time.Time
}

func (w Workspaces) db() *ent.Client { return ent.NewClient(ent.Driver(w.Drv)) }

// Of is every workspace an account is in, in the order it came to be in them,
// each by what it is called.
func (w Workspaces) Of(ctx context.Context, account pdid.Id) ([]Membership, error) {
	db := w.db()
	hs, err := db.Holder.Query().
		Where(holder.Account(account.Uuid()), holder.DateErasedIsNil()).
		WithTenant().
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ts := make([]*ent.Tenant, 0, len(hs))
	for _, h := range hs {
		ts = append(ts, h.Edges.Tenant)
	}
	names, err := Names(ctx, db, ts...)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(hs))
	for _, h := range hs {
		t := h.Edges.Tenant
		out = append(out, Membership{
			Workspace: pdid.Id(t.Id),
			Alias:     t.Alias,
			Name:      names[t.Id].Said,
			Saw:       names[t.Id].Saw,
			Holder:    pdid.Id(h.Id),
			Role:      h.Role,
			Closed:    t.DateClosed,
			Destroy:   t.DateDestroy,
		})
	}

	return out, nil
}

// Arrive is what an account signing in is given: a workspace of its own when
// it is in none and the deployment gives one, which is what signing up is.
//
// It has no name of its own (D57). Named after the person it is for, it would
// hold their name where nothing declares one -- on the trail as a value, and
// kept there when they are forgotten -- so it is called after its owner where
// it is read ([Names]).
func (w Workspaces) Arrive(ctx context.Context, p identity.Person) error {
	if !w.Personal {
		return nil
	}
	in, err := w.db().Holder.Query().Where(holder.Account(p.Id.Uuid()), holder.DateErasedIsNil()).Exist(ctx)
	if err != nil || in {
		return err
	}
	_, err = w.Create(ctx, p, "")

	return err
}

// Create makes a workspace an account owns, with a name it chose, or none to
// be called after its owner ([Names]), and an alias nobody chose -- changing
// it is a thing of its own (design 9.11) -- and what an asset register starts
// with. One transaction, so a workspace half made is not left to trip over.
func (w Workspaces) Create(ctx context.Context, owner identity.Person, name string) (Membership, error) {
	if w.Owned >= 0 {
		// A closed workspace is on its way out, and is not one more it has.
		n, err := w.db().Holder.Query().
			Where(holder.Account(owner.Id.Uuid()), holder.Role("owner"), holder.DateErasedIsNil(),
				holder.HasTenantWith(enttenant.DateClosedIsNil())).
			Count(ctx)
		if err != nil {
			return Membership{}, err
		}
		if n >= w.Owned {
			return Membership{}, ErrOwnsEnough
		}
	}

	// An address made up and found free, tried again when another workspace
	// took it in the meantime.
	aliases := w.Aliases
	if aliases.Db == nil {
		aliases.Db = w.db()
	}
	for range 5 {
		alias, err := aliases.Make(ctx)
		if err != nil {
			return Membership{}, err
		}
		m, err := w.create(ctx, owner, name, alias)
		if status.Code(err) == codes.AlreadyExists {
			continue
		}

		return m, err
	}

	return Membership{}, errors.New("no alias came free for a workspace")
}

func (w Workspaces) create(ctx context.Context, owner identity.Person, name, alias string) (Membership, error) {
	tdrv, tx, err := dialect.BeginTx(ctx, w.Drv)
	if err != nil {
		return Membership{}, err
	}
	defer tx.Rollback()

	base, err := enttx.Rebind(w.Base, tdrv)
	if err != nil {
		return Membership{}, err
	}
	t, err := base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: name}.Build())
	if err != nil {
		return Membership{}, err
	}
	tref := app.TenantRef_builder{Id: t.GetId()}.Build()
	who := owner.Name
	if who == "" {
		who = owner.Alias
	}
	h, err := base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  tref,
		Alias:   owner.Alias,
		Name:    who,
		Role:    z.Ptr("owner"),
		Account: owner.Id.Bytes(),
	}.Build())
	if err != nil {
		return Membership{}, fmt.Errorf("owner: %w", err)
	}
	if _, err := base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: tref,
		Name:   who,
		Kind:   "person",
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build()); err != nil {
		return Membership{}, err
	}

	ungated, err := enttx.Rebind(w.Ungated, tdrv)
	if err != nil {
		return Membership{}, err
	}
	tid, err := pdid.From(t.GetId())
	if err != nil {
		return Membership{}, err
	}
	hid, err := pdid.From(h.GetId())
	if err != nil {
		return Membership{}, err
	}
	// As its owner, so that what it starts with reads as theirs.
	as := frame.Into(ctx, frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))
	if err := SetUp(as, ungated); err != nil {
		return Membership{}, err
	}
	// Who made it, and from when: who pays for it, until somebody else says
	// they will (levyan#14).
	if w.Told != nil {
		if err := w.Told.Arrive(ctx, ent.NewClient(ent.Driver(tdrv)), tid, owner.Id, t.GetDateCreated().AsTime()); err != nil {
			return Membership{}, fmt.Errorf("levyan: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Membership{}, err
	}

	// Called as it will be read, now that its owner is there to be called
	// after.
	n, err := NameOf(ctx, w.db(), &ent.Tenant{Id: tid.Uuid(), Alias: alias, Name: name})
	if err != nil {
		return Membership{}, err
	}
	return Membership{Workspace: tid, Alias: alias, Name: n.Said, Saw: n.Saw, Holder: hid, Role: "owner"}, nil
}
