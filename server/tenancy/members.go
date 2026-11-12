package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/ent/predicate"
	"github.com/lesomnus/loft/internal/identity"
)

var (
	// ErrEnded is somebody whose membership was ended here: a login stopped
	// here stays stopped, whatever roster says of them, until somebody who
	// runs the workspace gives them one again.
	ErrEnded = errors.New("this membership was ended here")

	// ErrNoWorkspace is a sign-in into a deployment with no workspace yet,
	// or into a tenant this one does not serve: `loft init` puts the
	// workspace up.
	ErrNoWorkspace = errors.New("there is no workspace here for the tenant roster vouched in: `loft init` puts it up")

	// ErrRecord is a person record a member cannot be linked to: none of
	// this workspace's, or somebody else's already.
	ErrRecord = errors.New("not a person record a member can be")
)

// Addresses is what roster holds as checked of somebody: the addresses a
// person record here is found by ([identity.Store.Addresses]).
type Addresses interface {
	Addresses(ctx context.Context, holder pdid.Id) ([]string, error)
}

// Members is how somebody roster vouched for comes to be a member of the
// workspace (D85, #327's decision 11): the first time they sign in, with
// roster's identifier for them -- the holder they are in the tenant this
// deployment serves -- and as a member, which somebody who runs the workspace
// makes more of. The workspace's first owner is who `loft init` made it for.
type Members struct {
	// Base is the stack with no wall and no domain layer, and Drv the driver
	// it writes through: the deployment's own work, before there is anybody
	// to act as.
	Base app.Server
	Drv  dialect.Driver

	// People is roster, asked for the addresses it holds as checked of
	// somebody, which their person record here is found by. Nil finds none.
	People Addresses
}

// Of is the membership of somebody roster vouched for: theirs, or made now,
// in one transaction with the person record it is -- the one this workspace
// keeps already of somebody at an address roster holds as checked for them,
// when there is exactly one, or a new one. [ErrEnded] is a membership ended
// here, and [ErrNoWorkspace] a deployment with no workspace for the tenant.
func (m Members) Of(ctx context.Context, p identity.Person) (*ent.Holder, error) {
	db := ent.NewClient(ent.Driver(m.Drv))
	h, err := m.here(ctx, db, p)
	if err == nil || !errors.Is(err, errNotYet) {
		return h, err
	}

	if err := m.make(ctx, p); err != nil {
		// Two first sign-ins at once: the one that lost reads what the
		// other made.
		if h, e := m.here(ctx, db, p); e == nil {
			return h, nil
		}
		return nil, err
	}
	return m.here(ctx, db, p)
}

var errNotYet = errors.New("not a member yet")

func (m Members) here(ctx context.Context, db *ent.Client, p identity.Person) (*ent.Holder, error) {
	h, err := db.Holder.Get(ctx, p.Id.Uuid())
	switch {
	case ent.IsNotFound(err):
		return nil, errNotYet
	case err != nil:
		return nil, err
	case h.DateErased != nil:
		return nil, ErrEnded
	case pdid.Id(h.TenantId) != p.Tenant:
		return nil, fmt.Errorf("%w: a member of another workspace", ErrNoWorkspace)
	}
	return h, nil
}

func (m Members) make(ctx context.Context, p identity.Person) error {
	return m.admit(ctx, p, "member", pdid.Nil, "", true)
}

// Admit makes somebody roster has a member of the workspace with `role`: their
// membership by their identifier there (D85), and the person record they are
// -- `record`, one this workspace keeps that no login is linked to, or a new
// one called as roster calls them, with `email` on it. What an operator does
// at the roster in this process (`loft holder add`) and `init --demo`, which
// make the people at roster first: one transaction, the deployment's own
// work. Who comes to be a member by signing in is [Members.Of]'s.
func (m Members) Admit(ctx context.Context, p identity.Person, role string, record pdid.Id, email string) (*ent.Holder, error) {
	if err := m.admit(ctx, p, role, record, email, false); err != nil {
		return nil, err
	}
	return ent.NewClient(ent.Driver(m.Drv)).Holder.Get(ctx, p.Id.Uuid())
}

// admit is a new membership and the person record it is, in one transaction:
// `record` when it names one, the one found at an address roster checked when
// `find`, or a new one.
func (m Members) admit(ctx context.Context, p identity.Person, role string, record pdid.Id, email string, find bool) error {
	tdrv, tx, err := dialect.BeginTx(ctx, m.Drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	base, err := enttx.Rebind(m.Base, tdrv)
	if err != nil {
		return err
	}
	db := ent.NewClient(ent.Driver(tdrv))
	if _, err := db.Tenant.Get(ctx, p.Tenant.Uuid()); ent.IsNotFound(err) {
		return ErrNoWorkspace
	} else if err != nil {
		return err
	}
	tref := app.TenantRef_builder{Id: p.Tenant.Bytes()}.Build()

	// Called here as roster calls them, unless somebody here goes by that
	// already -- one whose holder at roster is gone and the name taken by
	// somebody new -- when it is that with a number after it.
	alias, err := freeAlias(ctx, db, p.Tenant, p.Alias)
	if err != nil {
		return err
	}
	name := or(p.Name, p.Alias)
	h, err := base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Id:     p.Id.Bytes(),
		Tenant: tref,
		Alias:  alias,
		Name:   name,
		Role:   z.Ptr(role),
	}.Build())
	if err != nil {
		return fmt.Errorf("@%s/%s: %w", p.TenantAlias, alias, err)
	}
	href := app.HolderRef_builder{Id: h.GetId()}.Build()

	var found *ent.Party
	switch {
	case !record.IsZero():
		v, err := db.Party.Query().
			Where(party.Id(record.Uuid()), party.TenantId(p.Tenant.Uuid()), party.Kind("person"), party.DateErasedIsNil()).
			Only(ctx)
		if ent.IsNotFound(err) {
			return fmt.Errorf("%w: no person record %s here", ErrRecord, record)
		}
		if err != nil {
			return err
		}
		if v.HolderId != (uuid.UUID{}) {
			return fmt.Errorf("%w: %s is a member's already", ErrRecord, record)
		}
		found = v
	case find:
		if found, err = m.record(ctx, db, p); err != nil {
			return err
		}
	}
	if found != nil {
		if _, err := base.Party().Patch(ctx, app.PartyPatchRequest_builder{
			Ref:              app.PartyRef_builder{Id: pdid.Id(found.Id).Bytes()}.Build(),
			Holder:           href,
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
	} else if _, err := base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: tref,
		Name:   name,
		Kind:   "person",
		Email:  strings.TrimSpace(email),
		Holder: href,
	}.Build()); err != nil {
		return err
	}

	return tx.Commit()
}

// record is the person record somebody new is, by an address roster holds as
// checked for them: a person here with no login, at one of those addresses,
// when there is exactly one -- more than one is nobody in particular, and
// gets a record of its own. An address nobody checked finds nobody, as
// whoever typed it could be anybody (nOAuth).
func (m Members) record(ctx context.Context, db *ent.Client, p identity.Person) (*ent.Party, error) {
	if m.People == nil {
		return nil, nil
	}
	as, err := m.People.Addresses(ctx, p.Id)
	if errors.Is(err, identity.ErrNoPerson) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at := []predicate.Party{}
	for _, a := range as {
		if a = strings.TrimSpace(a); a != "" {
			at = append(at, party.EmailEqualFold(a))
		}
	}
	if len(at) == 0 {
		return nil, nil
	}
	vs, err := db.Party.Query().
		Where(
			party.TenantId(p.Tenant.Uuid()),
			party.Kind("person"),
			party.Not(party.HasHolder()),
			party.DateErasedIsNil(),
			party.Or(at...),
		).
		Limit(2).
		All(ctx)
	if err != nil || len(vs) != 1 {
		return nil, err
	}
	return vs[0], nil
}

// freeAlias is what a new member is called in the workspace: what roster
// calls them, or that with a number after it while somebody here goes by it.
func freeAlias(ctx context.Context, db *ent.Client, tenant pdid.Id, want string) (string, error) {
	for i := 0; i < 20; i++ {
		alias := want
		if i > 0 {
			alias = fmt.Sprintf("%s-%d", want, i+1)
		}
		taken, err := db.Holder.Query().Where(holder.TenantId(tenant.Uuid()), holder.Alias(alias), holder.DateErasedIsNil()).Exist(ctx)
		if err != nil {
			return "", err
		}
		if !taken {
			return alias, nil
		}
	}
	return "", fmt.Errorf("too many members called %s here", want)
}
