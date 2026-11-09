package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/session"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/lifecycle"
	"github.com/lesomnus/loft/server/operator"
)

// operatorIdentity is `OperatorIdentityService`: who people are, which is
// roster's (design 9.10) -- what the shell's `loft identity` did on the box.
// The database is this build's shape already: `loft migrate` is the server's
// to run before it serves, and the entry point serves none that is not.
type operatorIdentity struct {
	app.UnimplementedOperatorIdentityServiceServer
	a Routes
}

// Migrate gives an account to every membership that names none (D27): a
// workspace restored from an export, whose members this deployment's roster
// does not have -- from another deployment, or forgotten here when the
// workspace was destroyed (`offboard.Restored.Unlinked`). Nobody of them can
// sign in until then. On the roster in this process it makes the directory
// when there is none and writes every such membership into it as an account
// with the identifier the membership has, so that what the trail says each did
// is still theirs; each is given a new password, said once, and the
// membership linked to it. A login another workspace has too gets that
// workspace's address beside it. Somebody roster has by that identifier
// already is linked as they are. A roster of its own is its operator's to
// write to, so for one this says what to make there, and links what was made.
// Each link is the operator's write.
func (s operatorIdentity) Migrate(req *app.OperatorIdentityMigrateRequest, out grpc.ServerStreamingServer[app.OperatorIdentityMigrateResponse]) error {
	ctx := operator.Frame(out.Context(), pdid.Nil)
	ts, err := s.a.S.Ent.Tenant.Query().Order(ent.Asc(enttenant.FieldAlias)).All(ctx)
	if err != nil {
		return err
	}
	send := func(h *ent.Holder, v *app.OperatorIdentityAdopted) error {
		access.Saw(ctx, access.Subject{Id: pdid.Id(h.Id), Tenant: pdid.Id(h.TenantId)})
		return out.Send(app.OperatorIdentityMigrateResponse_builder{Adopted: v}.Build())
	}
	done := app.OperatorIdentityMigrated_builder{Embedded: s.a.S.Identity.Embedded()}.Build()
	if s.a.S.Identity.Embedded() {
		err = s.adopt(ctx, ts, req.GetPassword(), send, done)
	} else {
		err = s.describe(ctx, ts, send, done)
	}
	if err != nil {
		return err
	}
	return out.Send(app.OperatorIdentityMigrateResponse_builder{Done: done}.Build())
}

// unlinked is everybody who signs in to a workspace here and whose holder
// names no account yet, in the order they were made.
func (s operatorIdentity) unlinked(ctx context.Context, t *ent.Tenant) ([]*ent.Holder, error) {
	return s.a.S.Ent.Holder.Query().
		Where(holder.TenantId(t.Id), holder.DateErasedIsNil(), holder.AccountIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
}

// link makes a holder the membership of the account with its own identifier,
// through the servers, so that the trail says it happened.
func (s operatorIdentity) link(ctx context.Context, h *ent.Holder) error {
	id := pdid.Id(h.Id)
	_, err := s.a.S.Base.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: id.Bytes()}.Build(),
		Account:          id.Bytes(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	return err
}

// freeAlias is a login for a holder in the directory: its own alias when
// nobody there has it, and otherwise that alias with its workspace's beside it.
func (s operatorIdentity) freeAlias(ctx context.Context, alias, workspace string, chosen map[string]bool) (string, error) {
	for i := 0; ; i++ {
		v := alias
		switch {
		case i == 1:
			v = alias + "-" + workspace
		case i > 1:
			v = fmt.Sprintf("%s-%s-%d", alias, workspace, i)
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

// aliasFree says whether nobody in the directory has a login. The
// administrator roster makes a directory with is nobody: no holder here is it,
// and it has no way to sign in.
func (s operatorIdentity) aliasFree(ctx context.Context, alias string) (bool, error) {
	p, err := s.a.S.Identity.Lookup(ctx, alias)
	if errors.Is(err, identity.ErrNoPerson) {
		return true, nil
	}
	if err != nil || alias != identity.RosterAdministrator {
		return false, err
	}
	used, err := s.a.S.Ent.Holder.Query().Where(holder.Or(holder.Account(p.Id.Uuid()), holder.Id(p.Id.Uuid()))).Exist(ctx)
	return !used, err
}

func (s operatorIdentity) adopt(ctx context.Context, ts []*ent.Tenant, password string, send func(*ent.Holder, *app.OperatorIdentityAdopted) error, done *app.OperatorIdentityMigrated) error {
	if _, err := s.a.S.Identity.EnsureDirectory(ctx); err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	chosen := map[string]bool{}
	for _, t := range ts {
		hs, err := s.unlinked(ctx, t)
		if err != nil {
			return err
		}
		people := []identity.Adoptee{}
		logins := map[uuid.UUID]string{}
		for _, h := range hs {
			p, err := s.a.S.Identity.ById(ctx, pdid.Id(h.Id).String())
			if err == nil {
				logins[h.Id] = p.Alias
				continue
			}
			if !errors.Is(err, identity.ErrNoPerson) {
				return fmt.Errorf("roster: %w", err)
			}
			alias, err := s.freeAlias(ctx, h.Alias, t.Alias, chosen)
			if err != nil {
				return fmt.Errorf("roster: %w", err)
			}
			chosen[alias] = true
			logins[h.Id] = alias
			people = append(people, identity.Adoptee{Id: pdid.Id(h.Id), Alias: alias, Name: h.Name})
		}
		passwords, err := s.a.S.Identity.Adopt(ctx, people, password)
		if err != nil {
			return err
		}
		for _, h := range hs {
			if err := s.link(ctx, h); err != nil {
				return err
			}
			login := logins[h.Id]
			v := app.OperatorIdentityAdopted_builder{Workspace: t.Alias, Member: h.Alias, Login: login}.Build()
			if pw, ok := passwords[login]; ok {
				v.SetPassword(pw)
				done.SetMade(done.GetMade() + 1)
			} else {
				v.SetLinked(true)
				done.SetLinked(done.GetLinked() + 1)
			}
			if err := send(h, v); err != nil {
				return err
			}
		}
	}
	done.SetGiven(password != "")
	return nil
}

func (s operatorIdentity) describe(ctx context.Context, ts []*ent.Tenant, send func(*ent.Holder, *app.OperatorIdentityAdopted) error, done *app.OperatorIdentityMigrated) error {
	d, err := s.a.S.Identity.Directory(ctx)
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	done.SetDirectory(d.Alias)
	for _, t := range ts {
		hs, err := s.unlinked(ctx, t)
		if err != nil {
			return err
		}
		for _, h := range hs {
			// The ones roster's operator made already, with these
			// identifiers, are linked; the rest are what to make.
			if _, err := s.a.S.Identity.ById(ctx, pdid.Id(h.Id).String()); err == nil {
				if err := s.link(ctx, h); err != nil {
					return err
				}
				done.SetLinked(done.GetLinked() + 1)
				if err := send(h, app.OperatorIdentityAdopted_builder{Workspace: t.Alias, Member: h.Alias, Linked: true}.Build()); err != nil {
					return err
				}
				continue
			} else if !errors.Is(err, identity.ErrNoPerson) {
				return fmt.Errorf("roster: %w", err)
			}
			body, _ := json.Marshal(map[string]string{"id": pdid.Id(h.Id).String(), "name": h.Name})
			make := fmt.Sprintf("roster holder add @%s/%s '%s'", d.Alias, h.Alias, strings.ReplaceAll(string(body), "'", `'\''`))
			done.SetTodo(done.GetTodo() + 1)
			if err := send(h, app.OperatorIdentityAdopted_builder{Workspace: t.Alias, Member: h.Alias, Make: make}.Build()); err != nil {
				return err
			}
		}
	}
	return nil
}

// accountOf is the account a request names: by its identifier, or by one of
// its memberships as @workspace/alias.
func (s operatorIdentity) accountOf(ctx context.Context, req *app.OperatorIdentityDepartRequest) (pdid.Id, error) {
	switch {
	case req.HasAccount():
		id, err := pdid.From(req.GetAccount())
		if err != nil {
			return pdid.Nil, status.Errorf(codes.InvalidArgument, "account: %v", err)
		}
		return id, nil
	case req.HasMember():
		member := strings.TrimSpace(req.GetMember())
		workspace, alias, ok := strings.Cut(strings.TrimPrefix(member, "@"), "/")
		if !ok || !strings.HasPrefix(member, "@") || workspace == "" || alias == "" {
			return pdid.Nil, status.Errorf(codes.InvalidArgument, "member: %q is not @workspace/alias", member)
		}
		h, err := s.a.S.Ent.Holder.Query().Where(
			holder.Alias(alias),
			holder.DateErasedIsNil(),
			holder.HasTenantWith(enttenant.Alias(workspace)),
		).Only(ctx)
		if ent.IsNotFound(err) {
			return pdid.Nil, status.Errorf(codes.NotFound, "member: nobody is %s", member)
		}
		if err != nil {
			return pdid.Nil, err
		}
		if h.Account == nil {
			return pdid.Nil, status.Errorf(codes.FailedPrecondition, "member: %s names no account; `identity migrate` first", member)
		}
		return pdid.Id(*h.Account), nil
	}
	return pdid.Nil, status.Error(codes.InvalidArgument, "which account: a member as @workspace/alias, or the account")
}

func (s operatorIdentity) workspaceAlias(ctx context.Context, id uuid.UUID) string {
	if t, err := s.a.S.Ent.Tenant.Get(ctx, id); err == nil {
		return "@" + t.Alias
	}
	return pdid.Id(id).String()
}

// Depart is an account leaving every workspace it is in, now (design 9.10),
// as `Lifecycle.Depart` does: each workspace's part the operator's work in
// it. The roster in this process is this deployment's alone, so an account
// that left every workspace leaves it too; one of its own is its operator's.
//
// It cannot be undone, so it says first, by the rules it then follows, what
// it would do in each workspace (`Lifecycle.Departing`): a dry run is that
// alone, and changes nothing. Either way the operator saw who the account is
// in every workspace, which the access record says.
func (s operatorIdentity) Depart(ctx context.Context, req *app.OperatorIdentityDepartRequest) (*app.OperatorIdentityDepartResponse, error) {
	who, err := s.accountOf(ctx, req)
	if err != nil {
		return nil, err
	}
	hs, err := s.a.S.Ent.Holder.Query().
		Where(holder.Account(who.Uuid()), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ahead, err := s.a.S.Lifecycle.Departing(ctx, who)
	if err != nil {
		return nil, err
	}
	would := map[pdid.Id]lifecycle.Departed{}
	for _, v := range ahead {
		would[v.Member] = v
	}

	out := app.OperatorIdentityDepartResponse_builder{Account: who.Bytes(), DryRun: req.GetDryRun(), Embedded: s.a.S.Identity.Embedded()}.Build()
	seen := []access.Subject{}
	for _, h := range hs {
		v := would[pdid.Id(h.Id)]
		m := app.OperatorIdentityMembership_builder{
			Workspace: s.workspaceAlias(ctx, h.TenantId),
			Member:    h.Alias,
			Role:      h.Role,
			Ownerless: v.Ownerless,
			Closes:    v.Closed,
		}.Build()
		seen = append(seen, access.Subject{Id: pdid.Id(h.Id), Tenant: pdid.Id(h.TenantId)})
		if !v.Owner.IsZero() {
			m.SetOwner(s.memberAlias(ctx, v.Owner))
			seen = append(seen, access.Subject{Id: v.Owner, Tenant: pdid.Id(h.TenantId)})
		}
		out.SetMembers(append(out.GetMembers(), m))
	}
	access.Saw(ctx, seen...)
	if req.GetDryRun() {
		return out, nil
	}

	gone, err := s.a.S.Lifecycle.Depart(ctx, who)
	for _, v := range gone {
		l := app.OperatorIdentityLeft_builder{
			Workspace: s.workspaceAlias(ctx, v.Workspace.Uuid()),
			Ownerless: v.Ownerless,
			Held:      uint32(v.Held),
			Closed:    v.Closed,
		}.Build()
		if !v.Owner.IsZero() {
			l.SetOwner(s.memberAlias(ctx, v.Owner))
			access.Saw(ctx, access.Subject{Id: v.Owner, Tenant: v.Workspace})
		}
		out.SetLeft(append(out.GetLeft(), l))
	}
	if err != nil {
		return nil, err
	}
	if s.a.S.Identity.Embedded() {
		if err := s.a.S.Identity.ForgetPerson(ctx, who); err != nil {
			return nil, err
		}
		out.SetForgot(true)
	}
	return out, nil
}

// memberAlias is a membership by its alias, or its identifier when it has
// none any more.
func (s operatorIdentity) memberAlias(ctx context.Context, member pdid.Id) string {
	if h, err := s.a.S.Ent.Holder.Get(ctx, member.Uuid()); err == nil && h.Alias != "" {
		return h.Alias
	}
	return member.String()
}

// Reconcile is `Lifecycle.Reconcile`, asked for now and told first.
func (s operatorIdentity) Reconcile(ctx context.Context, req *app.OperatorIdentityReconcileRequest) (*app.OperatorIdentityReconcileResponse, error) {
	v, err := s.a.S.Lifecycle.Reconcile(operator.Frame(ctx, pdid.Nil), !req.GetDryRun())
	if v == nil {
		return nil, err
	}
	members := func(hs []*ent.Holder) []string {
		vs := make([]string, 0, len(hs))
		for _, h := range hs {
			vs = append(vs, s.workspaceAlias(ctx, h.TenantId)+"/"+h.Alias)
			access.Saw(ctx, access.Subject{Id: pdid.Id(h.Id), Tenant: pdid.Id(h.TenantId)})
		}
		return vs
	}
	out := app.OperatorIdentityReconcileResponse_builder{
		DryRun:   req.GetDryRun(),
		Accounts: uint32(len(v.Accounts)),
		Found:    uint32(v.Found),
	}.Build()
	for _, a := range v.Gone {
		out.SetGone(append(out.GetGone(), app.OperatorIdentityAccount_builder{Account: a.Bytes(), Members: members(v.Members[a])}.Build()))
	}
	for _, st := range v.Back {
		out.SetBack(append(out.GetBack(), app.OperatorIdentityAccount_builder{Account: st.Holder.Bytes(), Members: members(v.Members[st.Holder])}.Build()))
	}
	return out, err
}

// operatorHolder is `OperatorHolderService`: an account and its memberships,
// on the roster in this process -- what the shell's `loft holder` did.
type operatorHolder struct {
	app.UnimplementedOperatorHolderServiceServer
	a Routes
}

// embedded refuses a deployment whose roster is one of its own.
func (s operatorHolder) embedded() error {
	if !s.a.S.Identity.Embedded() {
		return status.Error(codes.FailedPrecondition, "auth.roster names a roster of its own: accounts are made and given passwords there")
	}
	return nil
}

// Add puts an account in a workspace, making the account with a password said
// once when it is new: the membership and its person record written through
// the servers, as the operator's.
func (s operatorHolder) Add(ctx context.Context, req *app.OperatorHolderAddRequest) (*app.OperatorHolderAddResponse, error) {
	login := strings.TrimSpace(req.GetLogin())
	if login == "" {
		return nil, status.Error(codes.InvalidArgument, "login: what they sign in with")
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = login
	}
	role := strings.TrimSpace(req.GetRole())
	if role == "" {
		role = "member"
	}
	if !slices.Contains(domain.Roles, role) {
		return nil, status.Errorf(codes.InvalidArgument, "role: one of %s", strings.Join(domain.Roles, ", "))
	}
	if err := s.embedded(); err != nil {
		return nil, err
	}
	t, err := s.a.workspace(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}

	made, fresh := "", false
	p, err := s.a.S.Identity.Lookup(ctx, login)
	switch {
	case err == nil:
		if req.GetPassword() != "" {
			return nil, status.Errorf(codes.FailedPrecondition, "password: %s is an account already, with a password of its own; `holder password` changes it", login)
		}
	case errors.Is(err, identity.ErrNoPerson):
		p, err = s.a.S.Identity.AddPerson(ctx, login, name, pdid.Nil)
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

	if in, err := s.a.S.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Account(p.Id.Uuid()), holder.DateErasedIsNil()).Exist(ctx); err != nil {
		undo()
		return nil, err
	} else if in {
		return nil, status.Errorf(codes.AlreadyExists, "login: %s is in @%s already", login, t.Alias)
	}
	if taken, err := s.a.S.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Alias(p.Alias), holder.DateErasedIsNil()).Exist(ctx); err != nil {
		undo()
		return nil, err
	} else if taken {
		undo()
		return nil, status.Errorf(codes.AlreadyExists, "login: @%s/%s is somebody else already", t.Alias, p.Alias)
	}

	as := operator.Frame(ctx, pdid.Nil)
	tref := app.TenantRef_builder{Id: t.Id[:]}.Build()
	h, err := s.a.S.Base.Holder().Add(as, app.HolderAddRequest_builder{
		Tenant:  tref,
		Alias:   p.Alias,
		Name:    name,
		Role:    z.Ptr(role),
		Account: p.Id.Bytes(),
	}.Build())
	if err == nil {
		_, err = s.a.S.Base.Party().Add(as, app.PartyAddRequest_builder{
			Tenant: tref,
			Name:   name,
			Kind:   "person",
			Email:  strings.TrimSpace(req.GetEmail()),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build())
	}
	if err != nil {
		undo()
		return nil, err
	}
	return app.OperatorHolderAddResponse_builder{
		Workspace: t.Alias, Member: p.Alias, Account: p.Id.Bytes(), Role: role, Password: made,
	}.Build(), nil
}

// Password gives an account a new password, said once, and ends everywhere it
// is signed in -- here, in every workspace, and at roster.
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
		return nil, status.Errorf(codes.NotFound, "login: there is no account %s", login)
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
	if _, err := s.a.S.Ent.Session.Delete().Where(session.Account(p.Id.Uuid())).Exec(ctx); err != nil {
		return nil, err
	}
	return app.OperatorHolderPasswordResponse_builder{Login: p.Alias, Account: p.Id.Bytes(), Password: made}.Build(), nil
}
