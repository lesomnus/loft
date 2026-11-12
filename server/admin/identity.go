package admin

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/session"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/policy"
	"github.com/lesomnus/loft/server/tenancy"
)

// operatorIdentity is `OperatorIdentityService`: who the members are, which
// is roster's (design 9.10) -- what the shell's `loft identity` did on the
// box. The database is this build's shape already: `loft migrate` is the
// server's to run before it serves, and the entry point serves none that is
// not.
type operatorIdentity struct {
	app.UnimplementedOperatorIdentityServiceServer
	a Routes
}

// served is the workspace this deployment serves: the one of the tenant at
// roster, by its identifier (D85) -- or, on the roster in this process before
// it has the tenant, the one at the address the deployment serves, which
// `loft identity link` then makes the tenant of.
func (a Routes) served(ctx context.Context) (*ent.Tenant, error) {
	q := a.S.Ent.Tenant.Query()
	rt, err := a.S.Identity.Tenant(ctx)
	switch {
	case err == nil:
		q = q.Where(enttenant.Id(rt.Id.Uuid()))
	case errors.Is(err, identity.ErrNoTenant):
		if rt.Alias, err = a.S.Identity.TenantAlias(ctx); err != nil {
			return nil, fmt.Errorf("roster: %w", err)
		}
		q = q.Where(enttenant.Alias(rt.Alias))
	default:
		return nil, fmt.Errorf("roster: %w", err)
	}
	t, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, status.Errorf(codes.FailedPrecondition, "no workspace here is @%s, the tenant this deployment serves: `loft init` puts it up, or `loft tenant import` restores it", rt.Alias)
	}
	return t, err
}

// Link makes at roster every member of the workspace this deployment serves
// whom roster does not have, by the identifier they have here (D85): a
// workspace restored from an export, whose members this deployment's roster
// does not have -- from another deployment's, or forgotten at this one's
// since (`offboard.Restored.Unlinked`). Nobody of them can sign in until then.
//
// On the roster in this process it makes the tenant, by the workspace's
// identifier, when it is not there (D85) -- at the address the deployment
// serves, which is then the workspace's (D86) -- and writes every such member
// into it with the identifier they have here, so that what the trail says
// each did is still theirs; each is given a new password, said once. Somebody
// roster erased is said and made nobody: roster makes nobody else under an
// identifier it erased. A roster of its own is its operator's to write to, so
// for one this says what to make there. Somebody roster has already is left
// as they are.
func (s operatorIdentity) Link(req *app.OperatorIdentityLinkRequest, out grpc.ServerStreamingServer[app.OperatorIdentityLinkResponse]) error {
	ctx := operator.Frame(out.Context(), pdid.Nil)
	t, err := s.a.served(ctx)
	if err != nil {
		return err
	}
	people := s.a.S.Identity
	done := app.OperatorIdentityLinked_builder{Embedded: people.Embedded()}.Build()

	var rt identity.Tenant
	if people.Embedded() {
		// The tenant at roster is the workspace, by its identifier (D85):
		// made with it when it is not there, and its address the
		// workspace's (D86).
		if rt, err = people.EnsureTenant(ctx, identity.Tenant{Id: pdid.Id(t.Id), Name: t.Name}); err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		if _, err := tenancy.Follow(ctx, s.a.S.Base, rt); err != nil {
			return err
		}
	} else if rt, err = people.Tenant(ctx); err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	done.SetTenant(rt.Alias)

	hs, err := s.a.S.Ent.Holder.Query().
		Where(holder.TenantId(t.Id), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return err
	}
	send := func(h *ent.Holder, v *app.OperatorIdentityAdopted) error {
		access.Saw(ctx, access.Subject{Id: pdid.Id(h.Id), Tenant: pdid.Id(h.TenantId)})
		return out.Send(app.OperatorIdentityLinkResponse_builder{Adopted: v}.Build())
	}

	// The ones roster has, by their identifier, are theirs already.
	todo := []*ent.Holder{}
	for _, h := range hs {
		_, err := people.ById(ctx, pdid.Id(h.Id).String())
		switch {
		case err == nil:
			done.SetHad(done.GetHad() + 1)
		case errors.Is(err, identity.ErrNoPerson):
			todo = append(todo, h)
		default:
			return fmt.Errorf("roster: %w", err)
		}
	}

	if !people.Embedded() {
		for _, h := range todo {
			body, _ := json.Marshal(map[string]string{"id": pdid.Id(h.Id).String(), "name": h.Name})
			make := fmt.Sprintf("roster holder add @%s/%s '%s'", rt.Alias, h.Alias, strings.ReplaceAll(string(body), "'", `'\''`))
			done.SetTodo(done.GetTodo() + 1)
			if err := send(h, app.OperatorIdentityAdopted_builder{Member: h.Alias, Make: make}.Build()); err != nil {
				return err
			}
		}
		return out.Send(app.OperatorIdentityLinkResponse_builder{Done: done}.Build())
	}

	chosen := map[string]bool{}
	adoptees := make([]identity.Adoptee, 0, len(todo))
	logins := map[pdid.Id]string{}
	for _, h := range todo {
		alias, err := s.freeAlias(ctx, h.Alias, chosen)
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		chosen[alias] = true
		logins[pdid.Id(h.Id)] = alias
		adoptees = append(adoptees, identity.Adoptee{Id: pdid.Id(h.Id), Alias: alias, Name: h.Name})
	}
	v, err := people.Adopt(ctx, adoptees, req.GetPassword())
	if err != nil {
		return err
	}
	for _, h := range todo {
		id := pdid.Id(h.Id)
		a := app.OperatorIdentityAdopted_builder{Member: h.Alias}.Build()
		if slices.Contains(v.Erased, id) {
			a.SetErased(true)
			done.SetErased(done.GetErased() + 1)
		} else {
			login := logins[id]
			a.SetLogin(login)
			a.SetPassword(v.Passwords[login])
			done.SetMade(done.GetMade() + 1)
		}
		if err := send(h, a); err != nil {
			return err
		}
	}
	done.SetGiven(req.GetPassword() != "" && done.GetMade() > 0)
	return out.Send(app.OperatorIdentityLinkResponse_builder{Done: done}.Build())
}

// freeAlias is a login for a member in the tenant at roster: their own alias
// when nobody there has it, and otherwise that alias with a number after it.
func (s operatorIdentity) freeAlias(ctx context.Context, alias string, chosen map[string]bool) (string, error) {
	for i := 1; ; i++ {
		v := alias
		if i > 1 {
			v = fmt.Sprintf("%s-%d", alias, i)
		}
		if chosen[v] || v == identity.Agent {
			continue
		}
		free, err := s.aliasFree(ctx, v)
		if err != nil {
			return "", err
		}
		if free {
			return v, nil
		}
	}
}

// aliasFree says whether nobody in the tenant at roster has a login. The
// administrator roster makes a tenant with is nobody: no member here is it,
// and it has no way to sign in.
func (s operatorIdentity) aliasFree(ctx context.Context, alias string) (bool, error) {
	p, err := s.a.S.Identity.Lookup(ctx, alias)
	if errors.Is(err, identity.ErrNoPerson) {
		return true, nil
	}
	if err != nil || alias != identity.RosterAdministrator {
		return false, err
	}
	used, err := s.a.S.Ent.Holder.Query().Where(holder.Id(p.Id.Uuid())).Exist(ctx)
	return !used, err
}

// memberOf is the member a request names: by their identifier, or by their
// alias here.
func (a Routes) memberOf(ctx context.Context, v string) (*ent.Holder, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "@")
	if v == "" {
		return nil, status.Error(codes.InvalidArgument, "member: who -- their alias here, or their identifier")
	}
	q := a.S.Ent.Holder.Query().Where(holder.DateErasedIsNil())
	if id, err := pdid.Parse(v); err == nil {
		q = q.Where(holder.Id(id.Uuid()))
	} else {
		q = q.Where(holder.Alias(v))
	}
	h, err := q.Only(ctx)
	switch {
	case ent.IsNotFound(err):
		return nil, status.Errorf(codes.NotFound, "member: nobody here is %s", v)
	case ent.IsNotSingular(err):
		return nil, status.Errorf(codes.InvalidArgument, "member: more than one member here is %s; name one by their identifier", v)
	}
	return h, err
}

// Depart is a member leaving the workspace, now (design 9.10), as
// `Lifecycle.Depart` does: the operator's work in it. The roster in this
// process is this deployment's alone, so a member who left leaves it too; one
// of its own is its operator's.
//
// It cannot be undone, so it says first, by the rules it then follows, what it
// would do (`Lifecycle.Departing`): a dry run is that alone, and changes
// nothing. Either way the operator saw who the member is, which the access
// record says.
func (s operatorIdentity) Depart(ctx context.Context, req *app.OperatorIdentityDepartRequest) (*app.OperatorIdentityDepartResponse, error) {
	h, err := s.a.memberOf(ctx, req.GetMember())
	if err != nil {
		return nil, err
	}
	id := pdid.Id(h.Id)
	ahead, err := s.a.S.Lifecycle.Departing(ctx, id)
	if err != nil {
		return nil, err
	}

	out := app.OperatorIdentityDepartResponse_builder{
		Id: id.Bytes(), DryRun: req.GetDryRun(), Member: h.Alias,
		Embedded: s.a.S.Identity.Embedded(),
	}.Build()
	seen := []access.Subject{{Id: id, Tenant: pdid.Id(h.TenantId)}}
	if ahead != nil {
		out.SetCloses(ahead.Closed)
	}
	if req.GetDryRun() {
		access.Saw(ctx, seen...)
		return out, nil
	}

	gone, err := s.a.S.Lifecycle.Depart(ctx, id)
	if gone != nil {
		out.SetCloses(gone.Closed)
		out.SetHeld(uint32(gone.Held))
	}
	access.Saw(ctx, seen...)
	if err != nil {
		return nil, err
	}
	if s.a.S.Identity.Embedded() {
		if err := s.a.S.Identity.ForgetPerson(ctx, id); err != nil {
			return nil, err
		}
		out.SetForgot(true)
	}
	return out, nil
}

// Reconcile is `Lifecycle.Reconcile`, asked for now and told first.
func (s operatorIdentity) Reconcile(ctx context.Context, req *app.OperatorIdentityReconcileRequest) (*app.OperatorIdentityReconcileResponse, error) {
	v, err := s.a.S.Lifecycle.Reconcile(operator.Frame(ctx, pdid.Nil), !req.GetDryRun())
	if v == nil {
		return nil, err
	}
	member := func(id pdid.Id) *app.OperatorIdentityMember {
		m := app.OperatorIdentityMember_builder{Id: id.Bytes()}.Build()
		if h := v.Member(id); h != nil {
			m.SetMember(h.Alias)
			access.Saw(ctx, access.Subject{Id: id, Tenant: pdid.Id(h.TenantId)})
		}
		return m
	}
	out := app.OperatorIdentityReconcileResponse_builder{
		DryRun:  req.GetDryRun(),
		Members: uint32(len(v.Members)),
		Found:   uint32(v.Found),
	}.Build()
	for _, id := range v.Gone {
		out.SetGone(append(out.GetGone(), member(id)))
	}
	for _, st := range v.Back {
		out.SetBack(append(out.GetBack(), member(st.Holder)))
	}
	return out, err
}

// operatorHolder is `OperatorHolderService`: a member, on the roster in this
// process -- what the shell's `loft holder` did.
type operatorHolder struct {
	app.UnimplementedOperatorHolderServiceServer
	a Routes
}

// embedded refuses a deployment whose roster is one of its own.
func (s operatorHolder) embedded() error {
	if !s.a.S.Identity.Embedded() {
		return status.Error(codes.FailedPrecondition, "auth.roster names a roster of its own: people are made and given passwords there")
	}
	return nil
}

// Add makes somebody a member of the workspace this deployment serves: a
// holder of its tenant at the roster in this process -- made with a password
// said once when they are new there -- and their membership and person record
// here, written through the servers as the operator's, by the same
// identifier (D85). A membership ended here is taken up again (D88) -- while
// roster has them: one it has suspended is refused, which is roster's to
// undo first.
func (s operatorHolder) Add(ctx context.Context, req *app.OperatorHolderAddRequest) (*app.OperatorHolderAddResponse, error) {
	login := strings.TrimSpace(req.GetLogin())
	if login == "" {
		return nil, status.Error(codes.InvalidArgument, "login: what they sign in with")
	}
	name := strings.TrimSpace(req.GetName())
	// The role it is bound at the roster in this process, one Loft ships
	// (`policy.Roles`): on a roster of its own that is its tenant's
	// administrators', in roster's screens (D89).
	role := strings.TrimSpace(req.GetRole())
	if role == "" {
		role = "member"
	}
	if _, ok := policy.RoleOf(role); !ok {
		return nil, status.Errorf(codes.InvalidArgument, "role: one of owner, admin, manager, member, auditor -- the roles Loft ships, bound at the roster in this process")
	}
	if err := s.embedded(); err != nil {
		return nil, err
	}
	t, err := s.a.served(ctx)
	if err != nil {
		return nil, err
	}

	made, fresh := "", false
	p, err := s.a.S.Identity.Lookup(ctx, login)
	switch {
	case err == nil:
		if req.GetPassword() != "" {
			return nil, status.Errorf(codes.FailedPrecondition, "password: %s is at roster already, with a password of their own; `holder password` changes it", login)
		}
	case errors.Is(err, identity.ErrRefused):
		return nil, status.Errorf(codes.FailedPrecondition, "login: roster has %s suspended, which is roster's to undo first", login)
	case errors.Is(err, identity.ErrNoPerson):
		p, err = s.a.S.Identity.AddPerson(ctx, login, cmp.Or(name, login), pdid.Nil)
		if err != nil {
			return nil, fmt.Errorf("roster: %w", err)
		}
		fresh = true
		if pw := req.GetPassword(); pw != "" {
			err = s.a.S.Identity.SetPassword(ctx, p.Id, pw)
		} else {
			made, err = s.a.S.Identity.IssuePassword(ctx, p.Id)
		}
		if err != nil {
			s.a.S.Identity.ForgetPerson(ctx, p.Id)
			return nil, fmt.Errorf("roster: %w", err)
		}
	default:
		return nil, fmt.Errorf("roster: %w", err)
	}
	undo := func() {
		if fresh {
			s.a.S.Identity.ForgetPerson(ctx, p.Id)
		}
	}

	if was, err := s.a.S.Ent.Holder.Get(ctx, p.Id.Uuid()); err == nil {
		if was.DateErased == nil {
			undo()
			return nil, status.Errorf(codes.AlreadyExists, "login: %s is a member here already", login)
		}
	} else if !ent.IsNotFound(err) {
		undo()
		return nil, err
	}
	if taken, err := s.a.S.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Alias(p.Alias), holder.DateErasedIsNil()).Exist(ctx); err != nil {
		undo()
		return nil, err
	} else if taken {
		undo()
		return nil, status.Errorf(codes.AlreadyExists, "login: %s is somebody else here already", p.Alias)
	}

	// Their membership and person record, written as the operator's, the
	// way init --demo admits its people (`tenancy.Members.Admit`): called as
	// the operator said, or as roster calls them.
	p.Name = cmp.Or(name, p.Name, login)
	if _, err := s.a.S.Members.Admit(operator.Frame(ctx, pdid.Nil), p, role, pdid.Nil, req.GetEmail()); err != nil {
		undo()
		return nil, err
	}
	return app.OperatorHolderAddResponse_builder{
		Member: p.Alias, Id: p.Id.Bytes(), Role: role, Password: made,
	}.Build(), nil
}

// Password gives a member a new password, said once, and ends everywhere
// they are signed in -- here, and at roster.
func (s operatorHolder) Password(ctx context.Context, req *app.OperatorHolderPasswordRequest) (*app.OperatorHolderPasswordResponse, error) {
	login := strings.TrimSpace(req.GetLogin())
	if login == "" {
		return nil, status.Error(codes.InvalidArgument, "login: whose password")
	}
	if err := s.embedded(); err != nil {
		return nil, err
	}
	p, err := s.a.S.Identity.Lookup(ctx, login)
	if errors.Is(err, identity.ErrNoPerson) {
		return nil, status.Errorf(codes.NotFound, "login: nobody at roster signs in as %s", login)
	}
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	made := ""
	if pw := req.GetPassword(); pw != "" {
		err = s.a.S.Identity.SetPassword(ctx, p.Id, pw)
	} else {
		made, err = s.a.S.Identity.IssuePassword(ctx, p.Id)
	}
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	if err := s.a.S.Identity.Invalidate(ctx, p.Id); err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	if _, err := s.a.S.Ent.Session.Delete().Where(session.Holder(p.Id.Uuid())).Exec(ctx); err != nil {
		return nil, err
	}
	return app.OperatorHolderPasswordResponse_builder{Login: p.Alias, Id: p.Id.Bytes(), Password: made}.Build(), nil
}
