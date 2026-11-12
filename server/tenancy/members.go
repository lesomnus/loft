package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/reach"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/ent/predicate"
	"github.com/lesomnus/loft/internal/ent/session"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/policy"
)

var (
	// ErrNoWorkspace is a sign-in into a deployment with no workspace yet,
	// or into a tenant this one does not serve: `loft init` puts the
	// workspace up.
	ErrNoWorkspace = errors.New("there is no workspace here for the tenant roster vouched in: `loft init` puts it up")

	// ErrRecord is a person record a member cannot be linked to: none of
	// this workspace's, or somebody else's already.
	ErrRecord = errors.New("not a person record a member can be")

	// ErrMember is somebody who is a member here already.
	ErrMember = errors.New("a member here already")

	// ErrNoRole is somebody roster has in the tenant who may call nothing
	// here: no role of Loft's bound to them there (D89). They come no
	// further, and are no member for having tried.
	ErrNoRole = errors.New("roster binds them no role of Loft's")

	// ErrRole is a role Loft does not ship (`policy.Roles`).
	ErrRole = errors.New("not a role Loft ships: owner, admin, manager, member or auditor")
)

// People is roster, as a membership is made with it: the addresses it holds as
// checked of somebody, which a person record here is found by
// ([identity.Store.Addresses]), and the binding of somebody it made to a role
// ([identity.Store.Bind]).
type People interface {
	Addresses(ctx context.Context, holder pdid.Id) ([]string, error)
	Bind(ctx context.Context, role string, holder pdid.Id) error
	// Enters is whether roster binds somebody any role of Loft's at all
	// ([identity.Store.Enters]).
	Enters(ctx context.Context, holder pdid.Id) error
}

// Members is how somebody roster vouched for comes to be a member of the
// workspace (D85, #327's decision 11): the first time they sign in, with
// roster's identifier for them -- the holder they are in the tenant this
// deployment serves. What they may call is roster's too (D89): the roles the
// tenant binds them at roster, which a membership made here gives them none
// of. The workspace's first owner is who `loft init` bound the owner's role
// for.
//
// Who is a member is roster's (D87), so a membership ended here -- the person
// forgotten, or gone from roster past its grace -- is theirs again when
// roster vouches for them again (D88): it says they are one of the tenant.
type Members struct {
	// Base is the stack with no wall and no domain layer, and Drv the driver
	// it writes through: the deployment's own work, before there is anybody
	// to act as.
	Base app.Server
	Drv  dialect.Driver

	// People is roster, asked for the addresses it holds as checked of
	// somebody, which their person record here is found by, and told the role
	// of somebody [Members.Admit] makes. Nil finds none, and binds nobody.
	People People

	// Audit is what the trail keeps of a row (`audit.content`): a membership
	// taken up again is on it as that act ([Readmitted]).
	Audit audit.Contents
}

// Readmitted is the act a membership ended here and taken up again is on the
// trail as: the row is live again, which no server writes (D88).
const Readmitted = "loft.holder.readmit"

// Of is the membership of somebody roster vouched for: theirs, made now, or
// theirs again -- one ended here, taken up again ([Members.Admit]) -- in one
// transaction with the person record it is: the one this workspace keeps
// already of somebody at an address roster holds as checked for them, when
// there is exactly one, or a new one. [ErrNoWorkspace] is a deployment with
// no workspace for the tenant, and [ErrNoRole] somebody roster binds no role
// of Loft's, who comes no further (D89) -- asked at every sign-in, so that a
// membership whose roles roster took away signs in to nothing either, and one
// ended here is not taken up again for it.
func (m Members) Of(ctx context.Context, p identity.Person) (*ent.Holder, error) {
	db := ent.NewClient(ent.Driver(m.Drv))
	// The workspace first: before `loft init` there is nothing to come in
	// to, and roster has no role of Loft's to say so with.
	if _, err := db.Tenant.Get(ctx, p.Tenant.Uuid()); ent.IsNotFound(err) {
		return nil, ErrNoWorkspace
	} else if err != nil {
		return nil, err
	}
	h, err := m.here(ctx, db, p)
	if err != nil && !errors.Is(err, errNotYet) && !errors.Is(err, errEnded) {
		return nil, err
	}
	if err := m.enters(ctx, p.Id); err != nil {
		return nil, err
	}
	if h != nil {
		return h, nil
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

var (
	errNotYet = errors.New("not a member yet")
	errEnded  = errors.New("a membership ended here")
)

// enters is nil for somebody roster binds a role of Loft's, [ErrNoRole] for
// somebody it binds none, and a roster that could not be asked as itself.
func (m Members) enters(ctx context.Context, holder pdid.Id) error {
	if m.People == nil {
		return nil
	}
	err := m.People.Enters(ctx, holder)
	if errors.Is(err, reach.ErrDenied) {
		return ErrNoRole
	}
	return err
}

func (m Members) here(ctx context.Context, db *ent.Client, p identity.Person) (*ent.Holder, error) {
	h, err := db.Holder.Get(ctx, p.Id.Uuid())
	switch {
	case ent.IsNotFound(err):
		return nil, errNotYet
	case err != nil:
		return nil, err
	case h.DateErased != nil:
		return nil, errEnded
	case pdid.Id(h.TenantId) != p.Tenant:
		return nil, fmt.Errorf("%w: a member of another workspace", ErrNoWorkspace)
	}
	return h, nil
}

func (m Members) make(ctx context.Context, p identity.Person) error {
	return m.admit(ctx, p, pdid.Nil, "", true)
}

// Admit makes somebody roster has a member of the workspace: their membership
// by their identifier there (D85), and the person record they are -- `record`,
// one this workspace keeps that no login is linked to, or a new one called as
// roster calls them, with `email` on it -- and binds them at roster to `role`,
// one of the roles Loft ships by its word (`manager`, `policy.RoleOf`), unless
// it is empty. What an operator does at the roster in this process (`loft
// holder add`) and `init --demo`, which make the people at roster first: one
// transaction, the deployment's own work, and the binding after it. Who comes
// to be a member by signing in is [Members.Of]'s, and is bound nothing here.
//
// A membership ended here is taken up again (D88): the row it was, by the
// same identifier, is live again -- on the trail as [Readmitted], since no
// server writes a row back -- called as roster calls them now, leaving
// nothing and mailed as anybody new is; what it was signed in with before
// ends with it. A person record it was that was forgotten stays so, and it is
// given one found or made as anybody new is; one that was not, and is its
// own still, it keeps.
func (m Members) Admit(ctx context.Context, p identity.Person, role string, record pdid.Id, email string) (*ent.Holder, error) {
	r, ok := policy.Role{}, role == ""
	if !ok {
		if r, ok = policy.RoleOf(role); !ok {
			return nil, fmt.Errorf("%w: %q", ErrRole, role)
		}
	}
	if err := m.admit(ctx, p, record, email, false); err != nil {
		return nil, err
	}
	if r.Alias != "" && m.People != nil {
		if err := m.People.Bind(ctx, r.Alias, p.Id); err != nil {
			return nil, err
		}
	}
	return ent.NewClient(ent.Driver(m.Drv)).Holder.Get(ctx, p.Id.Uuid())
}

// admit is a new membership and the person record it is, in one transaction:
// `record` when it names one, the one found at an address roster checked when
// `find`, or a new one.
func (m Members) admit(ctx context.Context, p identity.Person, record pdid.Id, email string, find bool) error {
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

	ended := false
	switch was, err := db.Holder.Get(ctx, p.Id.Uuid()); {
	case ent.IsNotFound(err):
	case err != nil:
		return err
	case pdid.Id(was.TenantId) != p.Tenant:
		return fmt.Errorf("%w: a member of another workspace", ErrNoWorkspace)
	case was.DateErased == nil:
		return fmt.Errorf("@%s: %w", p.Alias, ErrMember)
	default:
		ended = true
	}

	// Called here as roster calls them, unless somebody here goes by that
	// already -- one whose holder at roster is gone and the name taken by
	// somebody new -- when it is that with a number after it.
	alias, err := freeAlias(ctx, db, p.Tenant, p.Alias)
	if err != nil {
		return err
	}
	name := or(p.Name, p.Alias)
	var h *app.Holder
	if ended {
		h, err = m.readmit(ctx, db, base, p.Id, alias, name)
	} else {
		h, err = base.Holder().Add(ctx, app.HolderAddRequest_builder{
			Id:     p.Id.Bytes(),
			Tenant: tref,
			Alias:  alias,
			Name:   name,
		}.Build())
	}
	if err != nil {
		return fmt.Errorf("@%s/%s: %w", p.TenantAlias, alias, err)
	}
	href := app.HolderRef_builder{Id: h.GetId()}.Build()

	// A membership ended with its person record left as it was -- not
	// forgotten -- is that record's still.
	if ended {
		switch _, err := db.Party.Query().Where(party.TenantId(p.Tenant.Uuid()), party.HolderId(p.Id.Uuid()), party.DateErasedIsNil()).First(ctx); {
		case err == nil:
			return tx.Commit()
		case !ent.IsNotFound(err):
			return err
		}
	}

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

// readmit makes the membership `member`, ended here, live again (D88): the
// row back through ent, and on the trail as the act it is, in the same
// transaction; the sessions it had, which a live row would answer to again,
// gone; and then, through the servers, what it is called, and nothing left
// of its leaving or of the choice its person made before. What it may call is
// what roster binds it (D89), which taking it up again leaves as it is.
func (m Members) readmit(ctx context.Context, db *ent.Client, base app.Server, member pdid.Id, alias, name string) (*app.Holder, error) {
	// Taken up once: a sign-in beside this one that took it up first has
	// it, and this one reads what that made.
	n, err := db.Holder.Update().Where(holder.Id(member.Uuid()), holder.DateErasedNotNil()).ClearDateErased().Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrMember
	}
	if _, err := pd.Record(ctx, db, m.Audit, Readmitted, []pdid.Id{member}); err != nil {
		return nil, err
	}
	if _, err := db.Session.Delete().Where(session.Holder(member.Uuid())).Exec(ctx); err != nil {
		return nil, err
	}
	return base.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: member.Bytes()}.Build(),
		Alias:            z.Ptr(alias),
		Name:             z.Ptr(name),
		DateLeavingNull:  z.Ptr(true),
		MailOff:          z.Ptr(false),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
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
