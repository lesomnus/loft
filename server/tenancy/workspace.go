package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
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

	// LabelSuffix is what a default label subdomain ends with; empty gives a
	// new workspace none.
	LabelSuffix string

	// Personal says whether an account in no workspace is given one when it
	// arrives.
	Personal bool
	// Owned is how many workspaces one account may own; negative is no limit.
	Owned int

	// Aliases is how a new workspace's address is made up.
	Aliases Aliases
}

// Membership is a workspace an account is in, and what it does there.
type Membership struct {
	Workspace pdid.Id
	Alias     string
	Name      string
	Holder    pdid.Id
	Role      string

	// Closed is when the workspace was closed, and Destroy when it goes if
	// it is not restored (D29); nil for one that is open.
	Closed, Destroy *time.Time
}

func (w Workspaces) db() *ent.Client { return ent.NewClient(ent.Driver(w.Drv)) }

// Of is every workspace an account is in, in the order it came to be in them.
func (w Workspaces) Of(ctx context.Context, account pdid.Id) ([]Membership, error) {
	hs, err := w.db().Holder.Query().
		Where(holder.Account(account.Uuid()), holder.DateErasedIsNil()).
		WithTenant().
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(hs))
	for _, h := range hs {
		t := h.Edges.Tenant
		out = append(out, Membership{
			Workspace: pdid.Id(t.Id),
			Alias:     t.Alias,
			Name:      t.Name,
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
func (w Workspaces) Arrive(ctx context.Context, p identity.Person) error {
	if !w.Personal {
		return nil
	}
	in, err := w.db().Holder.Query().Where(holder.Account(p.Id.Uuid()), holder.DateErasedIsNil()).Exist(ctx)
	if err != nil || in {
		return err
	}
	name := p.Name
	if name == "" {
		name = p.Alias
	}
	_, err = w.Create(ctx, p, name+"의 워크스페이스")

	return err
}

// Create makes a workspace an account owns, with a name it chose and an alias
// nobody chose -- changing it is a thing of its own (design 9.11) -- and what
// an asset register starts with. One transaction, so a workspace half made is
// not left to trip over.
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
	if err := SetUp(as, ungated, alias, w.LabelSuffix); err != nil {
		return Membership{}, err
	}
	if err := tx.Commit(); err != nil {
		return Membership{}, err
	}

	return Membership{Workspace: tid, Alias: alias, Name: name, Holder: hid, Role: "owner"}, nil
}
