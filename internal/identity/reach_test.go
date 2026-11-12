package identity

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/reach"
	"github.com/lesomnus/roster/rstr"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/policy"
)

// door is acme's administrator at roster, over the wire: who binds the roles
// Loft wrote there and changes them (D89) -- which is the only way roster's
// stream hears of it.
type door struct {
	ctx      context.Context
	tenant   pdid.Id
	roles    rstr.RoleServiceClient
	bindings rstr.BindingServiceClient
}

func (e *external) door(t *testing.T) door {
	t.Helper()
	conn := e.wire(t)
	return door{
		ctx:      auth.BearerProvider(e.admin).Provide(t.Context()),
		tenant:   e.acme,
		roles:    rstr.NewRoleServiceClient(conn),
		bindings: rstr.NewBindingServiceClient(conn),
	}
}

// role is one of acme's roles, by its alias.
func (d door) role(alias string) *rstr.RoleRef {
	return rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
		Alias:  z.Ptr(alias),
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
	}.Build()}.Build()
}

// bind binds `who` to the role `alias`, and answers the binding.
func (d door) bind(t *testing.T, alias string, who pdid.Id) *rstr.BindingRef {
	t.Helper()
	b, err := d.bindings.Add(d.ctx, rstr.BindingAddRequest_builder{
		Role:   d.role(alias),
		Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
	return rstr.BindingRef_builder{Id: b.GetId()}.Build()
}

func (d door) unbind(t *testing.T, b *rstr.BindingRef) {
	t.Helper()
	_, err := d.bindings.Erase(d.ctx, b)
	require.NoError(t, err)
}

// until asks `may` until it answers `want`: what the stream drops is asked
// again on the next call, and nothing says when the stream has.
func until(t *testing.T, may func() error, want error, why string) {
	t.Helper()
	require.Eventually(t, func() bool {
		err := may()
		if want == nil {
			return err == nil
		}
		return errors.Is(err, want)
	}, 10*time.Second, 20*time.Millisecond, why)
}

// opened returns once the follower hears roster. There is nothing to say a
// stream has subscribed, and a write made before it has is heard by nobody --
// so somebody nobody else asks about is bound a role of Loft's until they may
// come in, which is the stream open, and then has it taken away until that is
// heard: with a clock that does not move, what is kept of them changes from
// then only when the stream says so.
func opened(t *testing.T, e *external, s *Store, d door) {
	t.Helper()
	ctx := t.Context()
	h, err := e.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(), Alias: "canary", Name: "Canary",
	}.Build())
	require.NoError(t, err)
	canary, err := pdid.From(h.GetId())
	require.NoError(t, err)
	require.ErrorIs(t, s.Enters(ctx, canary), reach.ErrDenied)

	bound := d.bind(t, "loft-member", canary)
	until(t, func() error { return s.Enters(ctx, canary) }, nil, "the follower never opened its stream")
	require.Eventually(t, func() bool {
		if bound == nil {
			bound = d.bind(t, "loft-member", canary)
		}
		d.unbind(t, bound)
		bound = nil
		time.Sleep(50 * time.Millisecond)
		return errors.Is(s.Enters(ctx, canary), reach.ErrDenied)
	}, 10*time.Second, 10*time.Millisecond, "the follower never heard anything")
}

// TestWhatSomebodyMayCallIsWhatRosterSaysNow (D89): what somebody may call is
// what the roles roster binds them hold -- the five Loft wrote there, which are
// the tenant's from then, bound and changed by its administrators -- kept here
// for a while, and dropped as roster's stream says something of them changed,
// which every process of a deployment with a roster of its own follows
// (`cmd/serve.go`). So a role bound, taken away or narrowed at roster reaches
// the next call at once, and not once HeldTtl is over: the clock here does not
// move.
func TestWhatSomebodyMayCallIsWhatRosterSaysNow(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	s := e.open(t, e.key, "")
	now := time.Now()
	s.now = func() time.Time { return now }
	d := e.door(t)

	wrote, err := s.WriteRoles(ctx, policy.Roles)
	x.NoError(err)
	x.Len(wrote, len(policy.Roles))
	for _, r := range policy.Roles {
		v, err := d.roles.Get(d.ctx, rstr.RoleGetRequest_builder{
			Ref:    d.role(r.Alias),
			Select: rstr.RoleSelect_builder{Name: z.Ptr(true), Methods: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err, r.Alias)
		x.Equal(r.Name, v.GetName())
		x.ElementsMatch(r.Methods, v.GetMethods(), r.Alias)
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		s.Reach().Follow(ctx)
	}()
	opened(t, e, s, d)

	book, approve := app.ReservationService_Add_FullMethodName, app.ReservationService_Approve_FullMethodName
	may := func(method string) func() error {
		return func() error { return s.Reach().May(ctx, e.kim, method) }
	}
	x.ErrorIs(s.Enters(ctx, e.kim), reach.ErrDenied, "no role of Loft's: nothing here")
	member := d.bind(t, "loft-member", e.kim)
	until(t, func() error { return s.Enters(ctx, e.kim) }, nil, "a member at roster")
	x.NoError(may(book)())
	x.ErrorIs(may(approve)(), reach.ErrDenied)

	manager := d.bind(t, "loft-manager", e.kim)
	until(t, may(approve), nil, "a manager too")
	d.unbind(t, manager)
	until(t, may(approve), reach.ErrDenied, "and a manager no more")

	_, err = d.roles.Narrow(d.ctx, rstr.RoleNarrowRequest_builder{Ref: d.role("loft-member"), Methods: []string{book}}.Build())
	x.NoError(err)
	until(t, may(book), reach.ErrDenied, "the role they hold narrowed")
	x.NoError(s.Enters(ctx, e.kim), "and what is left of it")
	wrote, err = s.WriteRoles(ctx, policy.Roles)
	x.NoError(err)
	x.Empty(wrote, "a role the tenant has is the tenant's, as it made it")
	x.ErrorIs(may(book)(), reach.ErrDenied)

	d.unbind(t, member)
	until(t, func() error { return s.Enters(ctx, e.kim) }, reach.ErrDenied, "no role of Loft's left: nothing here")
}

// TestARoleIsWidenedAsLoftAsks (D90): a role Loft wrote at roster is given
// more as the holder Loft is there -- what a later release added -- and asking
// twice is asking once; a role the tenant took away is said, and nothing is
// made of it.
func TestARoleIsWidenedAsLoftAsks(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	s := e.open(t, e.key, "")
	d := e.door(t)

	_, err := s.WriteRoles(ctx, policy.Roles)
	x.NoError(err)
	approve := app.ReservationService_Approve_FullMethodName
	member, _ := policy.RoleOf("member")
	x.NotContains(member.Methods, approve)

	for range 2 {
		x.NoError(s.WidenRole(ctx, "loft-member", []string{approve}))
	}
	v, err := d.roles.Get(d.ctx, rstr.RoleGetRequest_builder{
		Ref:    d.role("loft-member"),
		Select: rstr.RoleSelect_builder{Methods: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.ElementsMatch(append(slices.Clone(member.Methods), approve), v.GetMethods(), "given it once, and nothing taken")

	err = s.WidenRole(ctx, "loft-nobody", []string{approve})
	x.ErrorIs(err, ErrRoleGone)
}
