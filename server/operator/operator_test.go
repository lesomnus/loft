package operator_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// TestAnOperatorIsTheirNameWhereverTheyAreRead: the same name is the same
// identifier in every process, an identifier pdid reads as one of
// [operator.Domain], and two names are two people.
func TestAnOperatorIsTheirNameWhereverTheyAreRead(t *testing.T) {
	x := require.New(t)

	alice := operator.Id("alice")
	x.Equal(alice, operator.Id("alice"))
	x.NotEqual(alice, operator.Id("bob"))
	x.Equal(operator.Domain, alice.Domain())
	_, ok := pdid.Of(alice.Uuid())
	x.True(ok, "laid out as every identifier is")
	x.True(operator.Is(alice))
	x.False(operator.Is(pdid.New(pd.HolderDomain)))
	x.False(operator.Is(pdid.Nil))
}

// TestNoEntityHasTheOperatorsDomain: an identifier of [operator.Domain] is an
// operator's and nothing else's -- a row of an entity numbered so would be
// read as somebody who acted. `pd entity add` numbers past the highest, so a
// schema that takes it took it by hand.
func TestNoEntityHasTheOperatorsDomain(t *testing.T) {
	x := require.New(t)
	x.NotEmpty(pd.Domains)
	for name, d := range pd.Domains {
		x.NotEqual(operator.Domain, d, "%s is numbered as an operator's identifier is", name)
	}
}

// TestTheDeploymentsWorkIsFramedAsTheOperators: what the deployment does in a
// workspace at an operator's word is framed as theirs there, and as nobody's
// with no operator -- the frame `domain.Deps.System` always made.
func TestTheDeploymentsWorkIsFramedAsTheOperators(t *testing.T) {
	x := require.New(t)
	tenant := pdid.New(pd.TenantDomain)

	f, ok := frame.From(operator.Frame(context.Background(), tenant))
	x.True(ok)
	x.True(f.Actor.IsZero(), "nobody's, with nobody's word")
	x.Equal(tenant, f.Tenant)
	x.Equal([]pdid.Id{tenant}, f.Scope.Ids())

	ctx := operator.Into(context.Background(), operator.Named("alice"))
	f, ok = frame.From(operator.Frame(ctx, tenant))
	x.True(ok)
	x.Equal(operator.Id("alice"), f.Actor)
	x.Equal(tenant, f.Tenant)

	f, ok = frame.From(operator.Frame(ctx, pdid.Nil))
	x.True(ok)
	x.Equal(operator.Id("alice"), f.Actor)
	x.True(f.Tenant.IsZero(), "of no workspace")
}
